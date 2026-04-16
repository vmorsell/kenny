package claude

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vmorsell/kenny/internal/lifecycle"
	"github.com/vmorsell/kenny/internal/metrics"
	"github.com/vmorsell/kenny/internal/state"
)

func writeFakeClaude(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	return path
}

func newTestRunner(t *testing.T, binary string) *Runner {
	t.Helper()
	ctx := context.Background()
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "kenny.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := lifecycle.New()
	m := metrics.Register(prometheus.NewRegistry(), clock, store, metrics.BuildInfo{SHA: "test", BuiltAt: "test"})

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(logger, m, Options{
		Binary:    binary,
		Cwd:       t.TempDir(),
		Env:       os.Environ(),
		WaitDelay: 2 * time.Second,
	})
}

func TestRunParsesStreamJSON(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-xyz"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"hello world"}]}}'
printf '%s\n' '{"type":"result","is_error":false,"result":"done"}'
exit 0
`
	binary := writeFakeClaude(t, script)
	r := newTestRunner(t, binary)

	res, err := r.Run(context.Background(), "hi", "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.SessionID != "sess-xyz" {
		t.Fatalf("SessionID = %q, want sess-xyz", res.SessionID)
	}
	if res.FinalText != "hello world" {
		t.Fatalf("FinalText = %q, want hello world", res.FinalText)
	}
	if res.EventCount != 3 {
		t.Fatalf("EventCount = %d, want 3", res.EventCount)
	}
}

func TestRunHandlesNonZeroExit(t *testing.T) {
	script := `#!/bin/sh
printf '%s\n' '{"type":"system"}'
exit 2
`
	binary := writeFakeClaude(t, script)
	r := newTestRunner(t, binary)

	res, err := r.Run(context.Background(), "hi", "")
	if err == nil {
		t.Fatalf("Run: expected error on non-zero exit")
	}
	if res.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2", res.ExitCode)
	}
}

func TestRunCancellationSendsSIGTERM(t *testing.T) {
	// Sleep for a long time; we'll cancel.
	script := `#!/bin/sh
trap 'exit 0' TERM
sleep 30 &
wait $!
`
	binary := writeFakeClaude(t, script)
	r := newTestRunner(t, binary)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var res *Result
	var runErr error
	go func() {
		res, runErr = r.Run(ctx, "hi", "")
		close(done)
	}()

	// Give the script time to start.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after cancel")
	}
	// We don't assert on the specific error; just that the run terminated.
	if res == nil {
		t.Fatalf("res nil")
	}
	_ = runErr
}
