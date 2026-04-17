package httpsrv

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vmorsell/kenny/internal/state"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "kenny.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Bind :0 to let the OS pick a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	reg := prometheus.NewRegistry()
	// Register a dummy collector so /metrics has content.
	reg.MustRegister(prometheus.NewCounter(prometheus.CounterOpts{Name: "x", Help: "x"}))

	s := New(addr, reg, store, StatusInfo{LifeID: 1})
	s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	waitReachable(t, addr)
	return s, addr
}

func waitReachable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never became reachable at %s", addr)
}

func TestHealthzBeforeReady(t *testing.T) {
	_, addr := newTestServer(t)
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("before ready: status = %d, want 503", resp.StatusCode)
	}
}

func TestHealthzAfterReady(t *testing.T) {
	s, addr := newTestServer(t)
	s.MarkReady()

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after ready: status = %d, want 200", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	_, addr := newTestServer(t)

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "# HELP") {
		t.Fatalf("body missing HELP header: %s", body)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	_, addr := newTestServer(t)

	// POST a message.
	postResp, err := http.Post("http://"+addr+"/api/message",
		"application/json", strings.NewReader(`{"content":"hello kenny"}`))
	if err != nil {
		t.Fatalf("POST /api/message: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d, want 202", postResp.StatusCode)
	}

	// GET pending messages.
	getResp, err := http.Get("http://" + addr + "/api/messages")
	if err != nil {
		t.Fatalf("GET /api/messages: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getResp.StatusCode)
	}
	body, _ := io.ReadAll(getResp.Body)
	if !strings.Contains(string(body), "hello kenny") {
		t.Fatalf("response missing message content: %s", body)
	}
}

func TestPostMessageEmptyBody(t *testing.T) {
	_, addr := newTestServer(t)
	resp, err := http.Post("http://"+addr+"/api/message",
		"application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty content: status = %d, want 400", resp.StatusCode)
	}
}

func TestNoteRoundTrip(t *testing.T) {
	_, addr := newTestServer(t)

	// Initially empty.
	r, _ := http.Get("http://" + addr + "/api/note")
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/note: status %d", r.StatusCode)
	}

	// Set a note.
	postR, err := http.Post("http://"+addr+"/api/note",
		"application/json", strings.NewReader(`{"content":"work on X"}`))
	if err != nil {
		t.Fatalf("POST /api/note: %v", err)
	}
	defer postR.Body.Close()
	if postR.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/note: status %d", postR.StatusCode)
	}

	// Read it back.
	r2, _ := http.Get("http://" + addr + "/api/note")
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if !strings.Contains(string(body), "work on X") {
		t.Fatalf("GET /api/note after set: %s", body)
	}

	// Delete it.
	req, _ := http.NewRequest(http.MethodDelete, "http://"+addr+"/api/note", nil)
	delR, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/note: %v", err)
	}
	delR.Body.Close()
	if delR.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /api/note: status %d", delR.StatusCode)
	}
}

func TestGetLivesEmpty(t *testing.T) {
	_, addr := newTestServer(t)
	resp, err := http.Get("http://" + addr + "/api/lives")
	if err != nil {
		t.Fatalf("GET /api/lives: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "[") {
		t.Fatalf("expected JSON array, got: %s", body)
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"hello", 200, "hello"},
		{"hello\nworld", 200, "hello"},
		{"\nhello", 200, "hello"},
		{"hello world", 5, "hello…"},
		{"Life #5 done.\n\n| table |", 200, "Life #5 done."},
		{"", 200, ""},
	}
	for _, c := range cases {
		got := firstLine(c.in, c.maxLen)
		if got != c.want {
			t.Errorf("firstLine(%q, %d) = %q, want %q", c.in, c.maxLen, got, c.want)
		}
	}
}

func TestGetInflight(t *testing.T) {
	_, addr := newTestServer(t)
	resp, err := http.Get("http://" + addr + "/api/inflight")
	if err != nil {
		t.Fatalf("GET /api/inflight: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// Empty store: should return a JSON array (empty or null).
	if !strings.Contains(string(body), "[") && !strings.Contains(string(body), "null") {
		t.Fatalf("expected JSON array, got: %s", body)
	}
}

func TestGetLivesWithData(t *testing.T) {
	ctx := context.Background()

	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "kenny2.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer store.Close()

	_ = store.AppendJournal(ctx, 1, "claude_success", "life one done")
	_ = store.AppendJournal(ctx, 2, "claude_success", "life two done")

	summaries, err := store.LifeSummaries(ctx, 10)
	if err != nil {
		t.Fatalf("LifeSummaries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want 2", len(summaries))
	}
	if summaries[0].LifeID != 2 || summaries[1].LifeID != 1 {
		t.Fatalf("unexpected order: %+v", summaries)
	}
}
