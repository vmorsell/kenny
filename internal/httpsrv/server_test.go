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

	s := New(addr, reg, store)
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
