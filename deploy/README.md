# Kenny deployment — Coolify

Kenny runs as a single service on Coolify built from the root `Dockerfile`. Observability runs alongside as five sibling services in the same Coolify project (so they share a network and Kenny is reachable at `kenny:8080`).

## Coolify services

### `kenny`

- **Build**: root `Dockerfile`. Build args `BUILD_SHA` and `BUILD_TIME` populate the `kenny_build_info` metric labels.
- **Persistent volume**: `/state` → named volume.
- **Healthcheck**: `GET /healthz` on port 8080, expecting `200`. Set a generous timeout (5–10s) so SQLite open + boot can complete.
- **Stop grace period**: `60s` so SIGTERM has room to drain the in-flight `claude -p` (see `internal/claude/runner.go` — `WaitDelay = 45s`).
- **Scheduled task**: cron `0 * * * *`, command `curl -X POST $COOLIFY_DEPLOY_WEBHOOK`. Triggers the hourly rebuild+redeploy that is Kenny's heartbeat.
- **Environment variables**:
  - `STATE_DIR=/state`
  - `HTTP_ADDR=:8080`
  - `REPO_DIR=/app`
  - `DEPLOY_INTERVAL_SECONDS=3600`
  - `GIT_USER_NAME=Kenny`
  - `GIT_USER_EMAIL=kenny@<your-domain>`
  - `GITHUB_REPO=vmorsell/kenny`
  - `GITHUB_PAT=<fine-grained PAT, contents:write on this repo only>`
  - `COOLIFY_DEPLOY_WEBHOOK=<this service's deploy webhook URL>`
  - Whatever env var the `claude` CLI uses for Max OAuth.

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
4. Trigger the first deploy manually — Kenny will journal "life #1 booted" and run `claude -p` once.
5. Set up the cron scheduled task on the Kenny service so the hourly heartbeat kicks in.
6. Open Grafana, log in, confirm the "Kenny's Life" dashboard is populated.
