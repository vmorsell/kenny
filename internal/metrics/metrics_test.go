package metrics

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/vmorsell/kenny/internal/lifecycle"
	"github.com/vmorsell/kenny/internal/state"
)

func TestRegisterAndScrape(t *testing.T) {
	ctx := context.Background()

	// Real store, real clock — exercise the full collector path.
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "kenny.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()
	if _, err := store.BeginLife(ctx); err != nil {
		t.Fatalf("BeginLife: %v", err)
	}
	if err := store.AppendJournal(ctx, 1, "boot", "hello"); err != nil {
		t.Fatalf("AppendJournal: %v", err)
	}

	clock := lifecycle.New()
	reg := prometheus.NewRegistry()
	m := Register(reg, clock, store, BuildInfo{SHA: "abc123", BuiltAt: "2026-04-16T10:00:00Z"})

	m.ClaudeInvocations.WithLabelValues("success").Inc()
	m.SelfModCommits.Inc()

	got, err := testutil.GatherAndLint(reg)
	if err != nil {
		t.Fatalf("GatherAndLint: %v", err)
	}
	// Lint emits Problem slice; non-fatal issues are OK but helpful to see.
	for _, p := range got {
		t.Logf("lint problem: %s: %s", p.Metric, p.Text)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	want := []string{
		"kenny_claude_invocations_total",
		"kenny_claude_invocation_duration_seconds",
		"kenny_self_mod_commits_total",
		"kenny_life_duration_seconds",
		"kenny_time_to_death_seconds",
		"kenny_life_count_total",
		"kenny_journal_entries_total",
		"kenny_inflight_tasks",
		"kenny_build_info",
	}
	present := map[string]bool{}
	for _, mf := range families {
		present[mf.GetName()] = true
	}
	var missing []string
	for _, name := range want {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("missing metrics: %s", strings.Join(missing, ", "))
	}

	if v := testutil.ToFloat64(m.ClaudeInvocations.WithLabelValues("success")); v != 1 {
		t.Fatalf("ClaudeInvocations{success} = %v, want 1", v)
	}
	if v := testutil.ToFloat64(m.SelfModCommits); v != 1 {
		t.Fatalf("SelfModCommits = %v, want 1", v)
	}
}
