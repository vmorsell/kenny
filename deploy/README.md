# Kenny deployment — Coolify

Kenny runs as a single Coolify service built from the root `Dockerfile`. Observability runs alongside as **five sibling services** in the same Coolify project (so they share a network and Kenny is reachable at `kenny:8080`). **Do not deploy the observability stack as a compose unit** — each service must be its own Coolify service so Kenny can be redeployed hourly without bouncing Grafana/Loki/Prometheus.

## Heartbeat

Kenny is forced to rebirth every hour via a **Coolify Scheduled Task** on the `kenny` service itself. No GitHub Actions, no empty commits, no Coolify API token.

- **Cron**: `0 * * * *`
- **Command**: `kill -TERM 1`

The task runs inside the running container. Signalling PID 1 (tini) forwards SIGTERM to Kenny, which graceful-shutdowns within the 60s `stop_grace_period`. Docker's `restart: unless-stopped` brings a fresh container up from the same image. Anything Kenny hasn't committed to git by then is wiped.

Source rebuilds happen independently: Coolify's auto-deploy-on-push reacts to any commit Kenny (or you) pushes to `main`, building a new image and replacing the running container. So the hourly rebirth restarts the current image; pushes produce a new image.

## Coolify services

### `kenny`

- **Build pack**: Docker Compose. Point Coolify at the root `docker-compose.yml` in this repo.
- **Source**: connect to this repo. Enable auto-deploy-on-push for `main`.
- **Why Compose instead of Dockerfile**: Coolify's Dockerfile build pack doesn't expose `stop_grace_period`. The compose file sets it to `60s` so SIGTERM has room to drain the in-flight `claude -p` (see `internal/claude/runner.go` — `WaitDelay = 45s`). Healthcheck + restart policy + volume are also defined there and inherited as-is.
- **Persistent volume**: declared in compose as `kenny-state` → mounted at `/state`. Coolify surfaces the volume in its Persistent Storage UI for backup.
- **Environment variables** (set these in Coolify's UI; compose substitutes them):
  - `GITHUB_REPO=vmorsell/kenny`
  - `GITHUB_PAT=<fine-grained PAT, contents:write on this repo only>`
  - `GIT_USER_NAME=Kenny` (optional)
  - `GIT_USER_EMAIL=kenny@<your-domain>` (optional)
  - `CLAUDE_CODE_OAUTH_TOKEN=<Claude Max OAuth>` (verify the exact var name with `claude --help`)
  - `BUILD_SHA=<commit sha>` and `BUILD_TIME=<iso8601>` optional — Coolify often injects the commit SHA automatically into the build context.

### `loki`

- **Image**: `grafana/loki:3.0.0`.
- **Config**: mount `deploy/loki/loki-config.yaml` at `/etc/loki/local-config.yaml`.
- **Persistent volume**: `/loki`.
- **Port**: 3100.

### `prometheus`

- **Image**: `prom/prometheus:v2.54.0`.
- **Config**: mount `deploy/prometheus/prometheus.yml` at `/etc/prometheus/prometheus.yml`.
- **Persistent volume**: `/prometheus`.
- **Port**: 9090.

### `grafana`

- **Image**: `grafana/grafana:11.1.0`.
- **Config**: mount `deploy/grafana/provisioning` at `/etc/grafana/provisioning` and `deploy/grafana/dashboards` at `/etc/grafana/dashboards`.
- **Persistent volume**: `/var/lib/grafana`.
- **Port**: 3000.
- **Env**: `GF_SECURITY_ADMIN_PASSWORD`, `GF_USERS_ALLOW_SIGN_UP=false`.

### `docker-socket-proxy`

- **Image**: `tecnativa/docker-socket-proxy:0.2.0`.
- **Mount**: `/var/run/docker.sock:/var/run/docker.sock:ro`.
- **Env**: `CONTAINERS=1`, `LOGS=1`. Everything else denied. This sidecar is the *only* service on Kenny's project that touches the Docker socket; Alloy goes through it.

### `alloy`

- **Image**: `grafana/alloy:v1.3.0`.
- **Config**: mount `deploy/alloy/config.alloy` at `/etc/alloy/config.alloy`.
- **Depends on**: `docker-socket-proxy`, `loki`.
- **Port**: 12345 (Alloy's own UI).

## Local development

```
cd deploy
docker compose -f docker-compose.observability.yml up
```

Then build and run Kenny pointing at the same network so `kenny:8080` resolves. Simplest: include Kenny as an additional service in a merged compose file, or run Kenny via `docker run --network=deploy_default ...` after the above is up.

## Bootstrap checklist

1. Create the Coolify project "kenny".
2. Add all six services as listed above.
3. Configure Kenny's environment variables. The fine-grained GitHub PAT must be scoped to `vmorsell/kenny` with `contents:write` only.
4. Trigger Kenny's first deploy manually — Kenny will journal "life #1 booted" and run `claude -p` once.
5. Add a Coolify Scheduled Task on the `kenny` service: cron `0 * * * *`, command `kill -TERM 1`. This is the hourly rebirth.
6. Open Grafana, log in, confirm the "Kenny's Life" dashboard is populated.
