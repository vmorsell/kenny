// Package httpsrv serves Kenny's /healthz and /metrics endpoints.
// /healthz is deep: it verifies the SQLite store is reachable and that
// Kenny's boot sequence finished. A shallow 200 while the SQLite volume
// is broken would defeat Coolify's auto-revert, so this matters.
package httpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vmorsell/kenny/internal/state"
)

type Server struct {
	srv   *http.Server
	store *state.Store
	ready atomic.Bool
}

// New wires up /healthz and /metrics. The server is created in a
// not-ready state; call MarkReady once boot is complete.
func New(addr string, reg *prometheus.Registry, store *state.Store) *Server {
	mux := http.NewServeMux()
	s := &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		store: store,
	}

	mux.HandleFunc("/healthz", s.healthz)
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/message", s.postMessage)

	return s
}

// MarkReady flips the readiness flag. /healthz returns 503 until this is called.
func (s *Server) MarkReady() { s.ready.Store(true) }

// Start runs the server in a goroutine and returns immediately.
func (s *Server) Start() {
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The caller can observe a permanent failure via Shutdown returning
			// or via the process exiting. We don't log here to avoid double
			// logging with main.go's slog sink.
			_ = err
		}
	}()
}

// Shutdown gracefully drains active connections.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

type healthBody struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !s.ready.Load() {
		writeHealth(w, http.StatusServiceUnavailable, "booting")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeHealth(w, http.StatusServiceUnavailable, "sqlite unreachable: "+err.Error())
		return
	}

	writeHealth(w, http.StatusOK, "")
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.store.AddMessage(ctx, body.Content); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func writeHealth(w http.ResponseWriter, status int, reason string) {
	w.WriteHeader(status)
	body := healthBody{Status: http.StatusText(status), Reason: reason}
	_ = json.NewEncoder(w).Encode(body)
}
