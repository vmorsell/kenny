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
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vmorsell/kenny/internal/state"
)

// StatusInfo is set once at boot and exposed via GET /api/status.
type StatusInfo struct {
	LifeID          int64
	BootAt          time.Time
	ExpectedDeathAt time.Time
}

type Server struct {
	srv    *http.Server
	store  *state.Store
	status StatusInfo
	ready  atomic.Bool
}

// New wires up /healthz and /metrics. The server is created in a
// not-ready state; call MarkReady once boot is complete.
func New(addr string, reg *prometheus.Registry, store *state.Store, status StatusInfo) *Server {
	mux := http.NewServeMux()
	s := &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		store:  store,
		status: status,
	}

	mux.HandleFunc("/healthz", s.healthz)
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/message", s.postMessage)
	mux.HandleFunc("GET /api/messages", s.getMessages)
	mux.HandleFunc("GET /api/journal", s.getJournal)
	mux.HandleFunc("GET /api/status", s.getStatus)

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

func (s *Server) getMessages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	msgs, err := s.store.PendingMessages(ctx)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	type msg struct {
		ReceivedAt string `json:"received_at"`
		Content    string `json:"content"`
	}
	out := make([]msg, len(msgs))
	for i, m := range msgs {
		out[i] = msg{ReceivedAt: m.ReceivedAt.Format(time.RFC3339), Content: m.Content}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) getJournal(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	entries, err := s.store.RecentJournal(ctx, limit)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	type entry struct {
		LifeID  int64  `json:"life_id"`
		At      string `json:"at"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	out := make([]entry, len(entries))
	for i, e := range entries {
		out[i] = entry{LifeID: e.LifeID, At: e.At.Format(time.RFC3339), Kind: e.Kind, Message: e.Message}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	remaining := s.status.ExpectedDeathAt.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	body := struct {
		LifeID           int64  `json:"life_id"`
		BootAt           string `json:"boot_at"`
		ExpectedDeathAt  string `json:"expected_death_at"`
		RemainingSeconds int64  `json:"remaining_seconds"`
	}{
		LifeID:           s.status.LifeID,
		BootAt:           s.status.BootAt.Format(time.RFC3339),
		ExpectedDeathAt:  s.status.ExpectedDeathAt.Format(time.RFC3339),
		RemainingSeconds: int64(remaining.Seconds()),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeHealth(w http.ResponseWriter, status int, reason string) {
	w.WriteHeader(status)
	body := healthBody{Status: http.StatusText(status), Reason: reason}
	_ = json.NewEncoder(w).Encode(body)
}
