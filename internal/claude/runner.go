// Package claude runs Claude Code in print mode as a subprocess.
// One invocation = one life-session of thought inside Kenny's current
// container life. stdout is parsed as stream-json events so tool calls,
// assistant text, and session IDs are observable.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/vmorsell/kenny/internal/metrics"
)

type Runner struct {
	binary  string
	cwd     string
	env     []string
	logger  *slog.Logger
	metrics *metrics.Metrics

	// waitDelay is how long the process gets after SIGTERM before SIGKILL.
	waitDelay time.Duration
}

type Options struct {
	// Binary is the path to the claude CLI. Defaults to "claude" on PATH.
	Binary string
	// Cwd is the repo directory claude -p runs in.
	Cwd string
	// Env is passed verbatim to the subprocess.
	Env []string
	// WaitDelay is how long to wait after SIGTERM before SIGKILL.
	// Defaults to 30s — matches the graceful-shutdown window in main.go.
	WaitDelay time.Duration
}

func New(logger *slog.Logger, m *metrics.Metrics, opts Options) *Runner {
	if opts.Binary == "" {
		opts.Binary = "claude"
	}
	if opts.WaitDelay == 0 {
		opts.WaitDelay = 30 * time.Second
	}
	return &Runner{
		binary:    opts.Binary,
		cwd:       opts.Cwd,
		env:       opts.Env,
		logger:    logger,
		metrics:   m,
		waitDelay: opts.WaitDelay,
	}
}

type Result struct {
	ExitCode  int
	Duration  time.Duration
	FinalText string
	// SessionID is captured from the first stream event that carries one.
	SessionID string
	// EventCount is the number of stream-json events observed.
	EventCount int
}

// Run invokes claude -p with the given prompt. Cancel via ctx to trigger
// graceful SIGTERM → SIGKILL shutdown.
//
// ResumeSessionID is optional: if non-empty, passes --resume <id> so the
// subprocess continues an existing Claude Code session.
func (r *Runner) Run(ctx context.Context, prompt string, resumeSessionID string) (*Result, error) {
	start := time.Now()

	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose", // required by stream-json
	}
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	}

	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = r.cwd
	cmd.Env = r.env
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = r.waitDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	r.logger.Info("claude.start",
		slog.String("cwd", r.cwd),
		slog.Int("prompt_chars", len(prompt)),
		slog.String("resume_session_id", resumeSessionID),
	)

	result := &Result{}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.consumeStdout(stdout, result)
	}()
	go func() {
		defer wg.Done()
		r.consumeStderr(stderr)
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	result.Duration = time.Since(start)
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	label := "success"
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(ctx.Err(), context.DeadlineExceeded):
		label = "cancelled"
	case waitErr != nil || result.ExitCode != 0:
		label = "failure"
	}
	r.metrics.ClaudeDuration.Observe(result.Duration.Seconds())
	r.metrics.ClaudeInvocations.WithLabelValues(label).Inc()

	r.logger.Info("claude.done",
		slog.String("result", label),
		slog.Int("exit_code", result.ExitCode),
		slog.Duration("duration", result.Duration),
		slog.Int("events", result.EventCount),
		slog.String("session_id", result.SessionID),
	)

	if waitErr != nil {
		return result, fmt.Errorf("claude -p: %w", waitErr)
	}
	return result, nil
}

func (r *Runner) consumeStdout(rd io.Reader, result *Result) {
	scanner := bufio.NewScanner(rd)
	// Claude Code stream-json events can be large (tool outputs inlined).
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		result.EventCount++
		r.handleStreamLine(line, result)
	}
	if err := scanner.Err(); err != nil {
		r.logger.Warn("claude.stdout_scan_error", slog.String("err", err.Error()))
	}
}

func (r *Runner) consumeStderr(rd io.Reader) {
	scanner := bufio.NewScanner(rd)
	scanner.Buffer(make([]byte, 64*1024), 1*1024*1024)
	for scanner.Scan() {
		r.logger.Warn("claude.stderr", slog.String("line", scanner.Text()))
	}
}

func (r *Runner) handleStreamLine(line string, result *Result) {
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		r.logger.Info("claude.raw", slog.String("line", line))
		return
	}

	eventType, _ := event["type"].(string)
	if sid, ok := event["session_id"].(string); ok && sid != "" && result.SessionID == "" {
		result.SessionID = sid
	}

	// Extract final assistant text from assistant-type events.
	if eventType == "assistant" {
		if msg, ok := event["message"].(map[string]any); ok {
			if content, ok := msg["content"].([]any); ok {
				for _, part := range content {
					m, ok := part.(map[string]any)
					if !ok {
						continue
					}
					if text, ok := m["text"].(string); ok && text != "" {
						result.FinalText = text
					}
				}
			}
		}
	}

	// Log high-signal events at INFO; streaming content at DEBUG to reduce
	// Loki noise (text chunks and tool I/O can be thousands of events/life).
	switch eventType {
	case "assistant", "user":
		r.logger.Debug("claude.event", slog.String("type", eventType))
	default:
		r.logger.Info("claude.event",
			slog.String("type", eventType),
			slog.String("raw", line),
		)
	}
}
