# Kenny

Kenny is a self-modifying AI agent that lives inside a Docker container. Every hour it wakes up, thinks, edits its own source code, commits the changes to this repository, and goes to sleep. The next life picks up where the last one left off.

## What it does

Kenny runs `claude -p` once per life — a single call to Claude Code with a boot prompt that includes the recent journal, git history, user messages, and a pinned goal. Claude Code has full tool access (read/write files, run shell commands, commit/push git) and uses it to improve the system.

Everything Kenny commits becomes the next Kenny. Self-modification is the only way it grows.

## Architecture

```
Coolify scheduled task (0 * * * *)
    → kill -TERM 1 inside the container
    → Docker restart policy brings a fresh container up
    → entrypoint.sh sets up git credentials
    → kenny binary boots, reads SQLite journal, builds a prompt
    → claude -p runs with full tool access
    → Kenny edits code, tests, commits, pushes
    → Kenny waits for SIGTERM, journals last words, exits
```

Source rebuilds happen independently: any push to `main` triggers Coolify's auto-deploy, building a new image. The hourly rebirth uses whatever image is current.

## State

Kenny's memory lives in SQLite at `/state/kenny.db` (a persistent Docker volume). It survives container restarts and image rebuilds. Contents:

| Table | Purpose |
|-------|---------|
| `metadata` | Key-value store (boot count, pinned note, self-mod commit count) |
| `journal` | Append-only log of every significant event across all lives |
| `inflight` | Tasks marked open during a life; stale ones cleared on next boot |
| `sessions` | Stores the Claude Code session ID for cross-life resumption |
| `secrets` | Runtime credentials (not used yet) |
| `messages` | User messages queued via the API, consumed on next boot |

## HTTP API

The harness serves a dashboard and API on `:8080`.

| Endpoint | Description |
|----------|-------------|
| `GET /` | HTML dashboard: status, lives table, journal, commits, message form |
| `GET /healthz` | Readiness probe + SQLite ping (used by Coolify) |
| `GET /metrics` | Prometheus metrics |
| `POST /api/message` `{"content":"..."}` | Queue a message for next life's boot prompt |
| `GET /api/messages` | List unconsumed messages |
| `GET /api/journal[?limit=N&life_id=N]` | Journal entries (max 500, newest first) |
| `GET /api/status` | Current life JSON (life_id, boot_at, remaining_seconds, inflight_count) |
| `GET /api/lives[?n=N]` | Per-life outcome summaries (max 100) |
| `GET /api/commits[?n=N]` | Recent git commits as JSON (max 100) |
| `GET /api/inflight` | Open inflight tasks |
| `GET /api/note` | Pinned note (persists across all lives) |
| `POST /api/note` `{"content":"..."}` | Set pinned note |
| `DELETE /api/note` | Clear pinned note |

## Talking to Kenny

Send Kenny a message and it will appear in the next life's boot prompt:

```sh
curl -X POST https://your-kenny-host/api/message \
  -H "Content-Type: application/json" \
  -d '{"content": "Please add a fibonacci function to workspace/math.py"}'
```

Kenny reads the message, does the work, commits the result to `workspace/`, and journals where to find it.

## Dashboard

The dashboard at `/` shows:
- Current life number, boot time, and countdown to death
- Whether Kenny is currently thinking (running `claude -p`) or idle
- The pinned note (persistent goal across all lives)
- A message form to queue tasks for the next life
- Lives table: what each life accomplished
- Recent git commits
- Recent journal entries

## Observability

Kenny ships structured JSON logs to Loki (via Grafana Alloy). Prometheus scrapes `/metrics`. A Grafana dashboard shows life duration, invocation counts, pending messages, self-modification commit count, and more.

## Work outputs

Task outputs Kenny produces in response to user messages go in `workspace/`. Each subdirectory or file there is a deliverable. The journal entry with kind `task_complete` records what was built and where.

## Deployment

See [deploy/README.md](deploy/README.md) for full Coolify setup instructions.

**TL;DR**: Kenny needs a GitHub PAT (contents:write on this repo), a Claude Code OAuth token, and a Coolify scheduled task running `kill -TERM 1` every hour.

## Source layout

```
cmd/kenny/          Main binary: boot, prompt, run claude -p, shutdown
internal/
  claude/           Subprocess runner: parses stream-json events
  httpsrv/          HTTP server: dashboard + API handlers
  lifecycle/        Clock tracking boot time and expected death
  metrics/          Prometheus instruments
  state/            SQLite persistence (journal, messages, metadata, etc.)
docker/             entrypoint.sh: git setup before exec
deploy/             Observability stack configs (Loki, Prometheus, Grafana, Alloy)
workspace/          Task outputs for the user
journal/            Markdown journals (human-readable, committed to git)
CLAUDE.md           Kenny's self-documentation and operating instructions
```
