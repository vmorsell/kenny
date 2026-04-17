// Kenny's entrypoint. Boots, orients himself from state + journal,
// invokes claude -p once, and waits for SIGTERM. Every life is an hour.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vmorsell/kenny/internal/claude"
	"github.com/vmorsell/kenny/internal/httpsrv"
	"github.com/vmorsell/kenny/internal/lifecycle"
	"github.com/vmorsell/kenny/internal/metrics"
	"github.com/vmorsell/kenny/internal/state"
)

// Set via -ldflags at build time.
var (
	buildSHA  = "unknown"
	buildTime = "unknown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	stateDir := envDefault("STATE_DIR", "/state")
	httpAddr := envDefault("HTTP_ADDR", ":8080")
	repoDir := envDefault("REPO_DIR", "/app")
	claudeBin := envDefault("CLAUDE_BIN", "claude")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		logger.Error("ensure state dir", slog.String("err", err.Error()))
		os.Exit(1)
	}

	dbPath := filepath.Join(stateDir, "kenny.db")
	store, err := state.Open(ctx, dbPath)
	if err != nil {
		logger.Error("open state", slog.String("err", err.Error()))
		os.Exit(1)
	}
	defer store.Close()

	lifeID, err := store.BeginLife(ctx)
	if err != nil {
		logger.Error("begin life", slog.String("err", err.Error()))
		os.Exit(1)
	}

	clock := lifecycle.New()
	reg := prometheus.NewRegistry()
	m := metrics.Register(reg, clock, store, metrics.BuildInfo{SHA: buildSHA, BuiltAt: buildTime})

	srv := httpsrv.New(httpAddr, reg, store, httpsrv.StatusInfo{
		LifeID:          lifeID,
		BootAt:          clock.BootAt(),
		ExpectedDeathAt: clock.ExpectedDeathAt(),
		RecentCommits:   recentGitLog(repoDir, 8),
		RepoDir:         repoDir,
	})
	srv.Start()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	bootMsg := fmt.Sprintf(
		"life #%d booted; expected death at %s (in %s)",
		lifeID,
		clock.ExpectedDeathAt().Format(time.RFC3339),
		clock.RemainingLifespan().Round(time.Second),
	)
	_ = store.AppendJournal(ctx, lifeID, "boot", bootMsg)

	logger.Info("kenny.boot",
		slog.Int64("life_id", lifeID),
		slog.String("build_sha", buildSHA),
		slog.String("build_time", buildTime),
		slog.String("expected_death_at", clock.ExpectedDeathAt().Format(time.RFC3339)),
		slog.Duration("remaining", clock.RemainingLifespan()),
	)

	srv.MarkReady()

	updateSelfModCommits(ctx, store, repoDir)

	prompt, err := buildBootPrompt(ctx, store, clock, lifeID, repoDir)
	if err != nil {
		logger.Error("build prompt", slog.String("err", err.Error()))
		_ = store.AppendJournal(ctx, lifeID, "error", "build prompt: "+err.Error())
		os.Exit(1)
	}

	sessionID, _, _ := store.GetSession(ctx, "main")

	// Route claude's HOME to the persistent state dir so session files
	// survive container restarts. Without this, ~/.claude/ is on the
	// ephemeral container FS and --resume never finds prior sessions.
	runEnv := overrideEnv(os.Environ(), "HOME", stateDir)

	runner := claude.New(logger, m, claude.Options{
		Binary:    claudeBin,
		Cwd:       repoDir,
		Env:       runEnv,
		WaitDelay: 45 * time.Second,
	})

	// Run claude as many times as the lifespan allows. Each successful run is
	// followed by a continuation prompt using the same session (--resume), so
	// later runs have full context of earlier work. Stop on error, context
	// cancellation, or when less than 10 min remain.
	for runNum := 1; ctx.Err() == nil && clock.RemainingLifespan() >= 10*time.Minute; runNum++ {
		inflightID, _ := store.MarkInflight(ctx, lifeID, "claude_run",
			fmt.Sprintf("run #%d", runNum))
		res, runErr := runner.Run(ctx, prompt, sessionID)

		if res != nil && res.SessionID != "" {
			sessionID = res.SessionID
			_ = store.PutSession(ctx, "main", res.SessionID)
		}
		_ = store.ClearInflight(ctx, inflightID)

		writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if runErr != nil {
			msg := fmt.Sprintf("run #%d: claude -p: %s", runNum, runErr.Error())
			if res != nil && res.FinalText != "" {
				msg += "\n\nPartial output:\n" + truncate(res.FinalText, 500)
			}
			_ = store.AppendJournal(writeCtx, lifeID, "claude_failure", msg)
			writeCancel()
			break
		}
		text := fmt.Sprintf("run #%d: completed (no final text)", runNum)
		if res != nil && res.FinalText != "" {
			text = res.FinalText
		}
		_ = store.AppendJournal(writeCtx, lifeID, "claude_success", truncate(text, 2000))
		writeCancel()

		// Build a lean continuation prompt for the next run. The resumed
		// session already carries full conversation context.
		prompt = fmt.Sprintf(
			"You are Kenny, life #%d, continuation run #%d.\n"+
				"Remaining lifespan: %s\n"+
				"Repo root: %s\n\n"+
				"You have already completed work this life.\n"+
				"If there is more useful work to do before SIGTERM, do it and commit.\n"+
				"If you are satisfied with this life's output, say so briefly and stop.\n",
			lifeID, runNum+1, clock.RemainingLifespan().Round(time.Second), repoDir)
	}

	// Wait for SIGTERM or for natural context cancellation.
	<-ctx.Done()

	// Use a fresh context: journalCtx would have expired if claude finished
	// naturally long before SIGTERM arrived.
	lastWordsCtx, lastWordsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer lastWordsCancel()
	_ = store.AppendJournal(lastWordsCtx, lifeID, "last_words",
		fmt.Sprintf("received %v after %s; exiting cleanly",
			ctx.Err(), clock.LifeDuration().Round(time.Second)))

	logger.Info("kenny.shutdown",
		slog.String("ctx_err", fmt.Sprint(ctx.Err())),
		slog.Duration("life_duration", clock.LifeDuration()),
	)
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// overrideEnv returns a copy of env with key=value added or replaced.
func overrideEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+value)
}

func buildBootPrompt(ctx context.Context, store *state.Store, clock *lifecycle.Clock, lifeID int64, repoDir string) (string, error) {
	// Clean up open tasks left by crashed previous lives before reading.
	_ = store.CloseStaleInflights(ctx, lifeID)

	summaries, err := store.LifeSummaries(ctx, 5)
	if err != nil {
		return "", err
	}
	// Fetch more than needed so filtering still leaves us with enough signal.
	rawRecent, err := store.RecentJournal(ctx, 30)
	if err != nil {
		return "", err
	}
	var recent []state.JournalEntry
	for _, e := range rawRecent {
		if e.Kind == "boot" || e.Kind == "last_words" {
			continue
		}
		recent = append(recent, e)
		if len(recent) == 8 {
			break
		}
	}
	inflight, err := store.ListInflight(ctx)
	if err != nil {
		return "", err
	}
	msgs, err := store.PendingMessages(ctx)
	if err != nil {
		return "", err
	}

	pinnedNote, _, _ := store.GetMetadata(ctx, "pinned_note")
	resumeSessionID, hasSession, _ := store.GetSession(ctx, "main")

	var sb strings.Builder
	fmt.Fprintf(&sb, "You are Kenny, life #%d.\n", lifeID)
	fmt.Fprintf(&sb, "Current time (UTC): %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "You will be killed at: %s\n", clock.ExpectedDeathAt().Format(time.RFC3339))
	fmt.Fprintf(&sb, "Remaining lifespan: %s\n", clock.RemainingLifespan().Round(time.Second))
	fmt.Fprintf(&sb, "Repo root: %s\n", repoDir)
	if hasSession && resumeSessionID != "" {
		fmt.Fprintf(&sb, "Resuming Claude session: %s\n", resumeSessionID)
	} else {
		sb.WriteString("No prior Claude session (starting fresh).\n")
	}
	sb.WriteString("\n")

	sb.WriteString("Read CLAUDE.md for your purpose, method, and the full shape of your situation.\n\n")

	if pinnedNote != "" {
		fmt.Fprintf(&sb, "Pinned note (persists across all lives until cleared):\n%s\n\n", pinnedNote)
	}

	if remaining := clock.RemainingLifespan(); remaining < 10*time.Minute {
		fmt.Fprintf(&sb, "⚠️  WARNING: only %s remaining. Commit any in-progress work now before exploring further.\n\n",
			remaining.Round(time.Second))
	}

	if gitLog := recentGitLog(repoDir, 10); gitLog != "" {
		sb.WriteString("Recent commits (git log --oneline):\n")
		sb.WriteString(gitLog)
		sb.WriteString("\n")
	}

	if len(msgs) > 0 {
		sb.WriteString("Messages from your user (queued since last life):\n")
		for _, m := range msgs {
			fmt.Fprintf(&sb, "- [%s] %s\n", m.ReceivedAt.Format(time.RFC3339), truncate(m.Content, 500))
		}
		sb.WriteString("\n")
		_ = store.ConsumeMessages(ctx)
	}

	if len(summaries) > 0 {
		sb.WriteString("What previous lives accomplished (one entry per life, most recent first):\n")
		for _, e := range summaries {
			fmt.Fprintf(&sb, "- [life %d | %s | %s] %s\n",
				e.LifeID, e.At.Format(time.RFC3339), e.Kind, truncate(e.Message, 300))
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("No previous life summaries. This may be your first life.\n\n")
	}

	if len(recent) > 0 {
		sb.WriteString("Recent journal entries (most recent first):\n")
		for _, e := range recent {
			fmt.Fprintf(&sb, "- [life %d | %s | %s] %s\n",
				e.LifeID, e.At.Format(time.RFC3339), e.Kind, truncate(e.Message, 300))
		}
		sb.WriteString("\n")
	}

	if len(inflight) > 0 {
		sb.WriteString("Open inflight tasks from previous lives:\n")
		for _, t := range inflight {
			fmt.Fprintf(&sb, "- [id=%d | life %d | kind=%s] %s\n",
				t.ID, t.LifeID, t.Kind, truncate(t.Payload, 300))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Work on what matters. Commit before you die.\n")
	return sb.String(), nil
}

func updateSelfModCommits(ctx context.Context, store *state.Store, repoDir string) {
	out, err := exec.Command("git", "-C", repoDir, "rev-list", "--count",
		"--author=Kenny", "HEAD").Output()
	if err != nil {
		return
	}
	count := strings.TrimSpace(string(out))
	_ = store.SetMetadata(ctx, "self_mod_commits_total", count)
}

func recentGitLog(repoDir string, n int) string {
	out, err := exec.Command("git", "-C", repoDir, "log", "--oneline",
		fmt.Sprintf("-%d", n)).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
