// Package httpsrv serves Kenny's HTTP endpoints.
// /healthz is deep: it verifies the SQLite store is reachable and that
// Kenny's boot sequence finished. A shallow 200 while the SQLite volume
// is broken would defeat Coolify's auto-revert, so this matters.
package httpsrv

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
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
	RecentCommits   string // output of git log --oneline -5
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

	mux.HandleFunc("/", s.dashboard)
	mux.HandleFunc("/healthz", s.healthz)
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/message", cors(s.postMessage))
	mux.HandleFunc("GET /api/messages", cors(s.getMessages))
	mux.HandleFunc("GET /api/journal", cors(s.getJournal))
	mux.HandleFunc("GET /api/status", cors(s.getStatus))
	mux.HandleFunc("OPTIONS /api/", cors(func(w http.ResponseWriter, r *http.Request) {}))

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

// cors wraps a handler with CORS headers so the API is callable from browsers.
func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
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
	msg, err := s.store.AddMessage(ctx, body.Content)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"received_at": msg.ReceivedAt.Format(time.RFC3339),
		"content":     msg.Content,
	})
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
	var lifeID int64
	if v := r.URL.Query().Get("life_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			lifeID = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	entries, err := s.store.RecentJournal(ctx, limit, lifeID)
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

var dashTmpl = template.Must(template.New("dash").Parse(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Kenny</title>
<style>
body{font-family:monospace;max-width:860px;margin:2rem auto;padding:0 1rem;color:#cdd6f4;background:#1e1e2e}
h1{color:#89b4fa;margin-bottom:0}
.sub{color:#6c7086;font-size:.85em;margin-bottom:1.5rem}
.status{background:#181825;border:1px solid #313244;border-radius:6px;padding:1rem;margin-bottom:1.5rem}
.status dl{display:grid;grid-template-columns:max-content 1fr;gap:.25rem .75rem;margin:0}
.status dt{color:#6c7086}.status dd{margin:0}
#countdown{color:#a6e3a1}
table{width:100%;border-collapse:collapse;font-size:.85em}
th{text-align:left;border-bottom:1px solid #313244;padding:.3rem .5rem;color:#6c7086}
td{padding:.3rem .5rem;border-bottom:1px solid #181825;vertical-align:top;word-break:break-word}
td:first-child,td:nth-child(2){white-space:nowrap;color:#6c7086}
.kind-boot{color:#a6e3a1}.kind-claude_success{color:#89b4fa}
.kind-claude_failure{color:#f38ba8}.kind-last_words{color:#fab387}
h2{color:#89b4fa;margin-top:1.5rem;margin-bottom:.5rem;font-size:1em}
.api{background:#181825;border:1px solid #313244;border-radius:6px;padding:.75rem 1rem;font-size:.85em;line-height:1.8}
.msg-form{display:flex;gap:.5rem;margin-top:1.5rem}
.msg-form textarea{flex:1;background:#181825;border:1px solid #313244;border-radius:4px;color:#cdd6f4;padding:.5rem;font-family:monospace;font-size:.9em;resize:vertical;min-height:60px}
.msg-form button{background:#89b4fa;color:#1e1e2e;border:none;border-radius:4px;padding:.5rem 1rem;cursor:pointer;font-family:monospace;font-weight:bold;align-self:flex-end}
.msg-form button:hover{background:#74c7ec}
#msg-status{font-size:.8em;margin-top:.3rem;color:#a6e3a1}
</style></head>
<body>
<h1>Kenny</h1>
<p class="sub">Self-modifying AI agent &mdash; life #{{.LifeID}}</p>

<div class="status">
<dl>
  <dt>Life</dt><dd>#{{.LifeID}}</dd>
  <dt>Boot</dt><dd>{{.BootAt}}</dd>
  <dt>Expected death</dt><dd>{{.ExpectedDeathAt}}</dd>
  <dt>Remaining</dt><dd><span id="countdown">{{.RemainingSeconds}}s</span></dd>
  <dt>Pending messages</dt><dd id="pending-count">{{.PendingCount}}</dd>
</dl>
</div>

<h2>Send a message to next life</h2>
<div class="msg-form">
  <textarea id="msg-input" placeholder="What should Kenny work on?"></textarea>
  <button onclick="sendMessage()">Send</button>
</div>
<div id="msg-status"></div>

{{if .RecentCommits}}<h2>Recent commits</h2>
<div class="api" style="white-space:pre">{{.RecentCommits}}</div>
{{end}}<h2>Recent journal</h2>
<table>
<tr><th>Life</th><th>Time</th><th>Kind</th><th>Message</th></tr>
<tbody id="journal-body">
{{range .Journal}}<tr>
  <td>{{.LifeID}}</td>
  <td>{{.At}}</td>
  <td class="kind-{{.Kind}}">{{.Kind}}</td>
  <td>{{.Message}}</td>
</tr>{{end}}
</tbody>
</table>

<h2>API</h2>
<div class="api">
POST /api/message &nbsp;{"content":"..."} &mdash; queue a message for next life<br>
GET &nbsp;/api/messages &mdash; list unconsumed messages<br>
GET &nbsp;/api/journal[?limit=N] &mdash; journal entries (max 500)<br>
GET &nbsp;/api/status &mdash; current life info (JSON)<br>
GET &nbsp;/healthz &mdash; readiness + SQLite check<br>
GET &nbsp;/metrics &mdash; Prometheus
</div>

<script>
let secs = {{.RemainingSeconds}};
const cd = document.getElementById('countdown');
setInterval(() => { if(secs > 0) secs--; cd.textContent = secs + 's'; }, 1000);

async function sendMessage() {
  const ta = document.getElementById('msg-input');
  const st = document.getElementById('msg-status');
  const content = ta.value.trim();
  if (!content) return;
  st.textContent = 'Sending…';
  st.style.color = '#6c7086';
  try {
    const r = await fetch('/api/message', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({content})
    });
    if (r.ok) {
      ta.value = '';
      st.textContent = 'Queued. Kenny will see it next life.';
      st.style.color = '#a6e3a1';
      const pc = document.getElementById('pending-count');
      pc.textContent = parseInt(pc.textContent||'0') + 1;
    } else {
      st.textContent = 'Error: ' + r.status;
      st.style.color = '#f38ba8';
    }
  } catch(e) {
    st.textContent = 'Network error: ' + e.message;
    st.style.color = '#f38ba8';
  }
}

async function refreshJournal() {
  try {
    const r = await fetch('/api/journal?limit=20');
    if (!r.ok) return;
    const entries = await r.json();
    const tbody = document.getElementById('journal-body');
    if (!tbody) return;
    const kindColor = {boot:'#a6e3a1',claude_success:'#89b4fa',claude_failure:'#f38ba8',last_words:'#fab387'};
    tbody.innerHTML = entries.map(e => {
      const at = e.at.replace('T',' ').substring(5,16);
      const color = kindColor[e.kind] || '#cdd6f4';
      const msg = (e.message||'').substring(0,200);
      return '<tr><td>'+e.life_id+'</td><td>'+at+'</td><td style="color:'+color+'">'+e.kind+'</td><td>'+escHtml(msg)+'</td></tr>';
    }).join('');
  } catch(_) {}
}
function escHtml(s){return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');}
setInterval(refreshJournal, 30000);
</script>
</body></html>
`))

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	entries, _ := s.store.RecentJournal(ctx, 20)
	pending, _ := s.store.PendingMessages(ctx)

	now := time.Now().UTC()
	remaining := s.status.ExpectedDeathAt.Sub(now)
	if remaining < 0 {
		remaining = 0
	}

	type row struct {
		LifeID  int64
		At      string
		Kind    string
		Message string
	}
	rows := make([]row, len(entries))
	for i, e := range entries {
		msg := e.Message
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		rows[i] = row{LifeID: e.LifeID, At: e.At.Format("01-02 15:04"), Kind: e.Kind, Message: msg}
	}

	data := struct {
		LifeID           int64
		BootAt           string
		ExpectedDeathAt  string
		RemainingSeconds int64
		PendingCount     int
		RecentCommits    string
		Journal          []row
	}{
		LifeID:           s.status.LifeID,
		BootAt:           s.status.BootAt.Format(time.RFC3339),
		ExpectedDeathAt:  s.status.ExpectedDeathAt.Format(time.RFC3339),
		RemainingSeconds: int64(remaining.Seconds()),
		PendingCount:     len(pending),
		RecentCommits:    s.status.RecentCommits,
		Journal:          rows,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashTmpl.Execute(w, data)
}

func writeHealth(w http.ResponseWriter, status int, reason string) {
	w.WriteHeader(status)
	body := healthBody{Status: http.StatusText(status), Reason: reason}
	_ = json.NewEncoder(w).Encode(body)
}
