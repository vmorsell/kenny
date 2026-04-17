# syntax=docker/dockerfile:1.7

# -----------------------------------------------------------------------------
# Builder — compile Kenny.
# -----------------------------------------------------------------------------
FROM golang:1.25-bookworm AS builder

WORKDIR /src

# Prime the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BUILD_SHA=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w -X main.buildSHA=${BUILD_SHA} -X main.buildTime=${BUILD_TIME}" \
    -o /out/kenny ./cmd/kenny

# -----------------------------------------------------------------------------
# Runtime — Go toolchain + Node + Claude Code + git. Heavier than a scratch
# image, but Claude Code runs inside this container (for self-modification)
# and needs all three.
# -----------------------------------------------------------------------------
FROM node:22-bookworm-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gosu \
        tini \
    && rm -rf /var/lib/apt/lists/*

# Copy the Go toolchain from the builder image so Claude Code can run
# `go build` / `go test` inside Kenny's container when self-modifying.
COPY --from=builder /usr/local/go /usr/local/go
# Copy the module cache so the entrypoint's self-update rebuild works offline.
COPY --from=builder /go/pkg/mod /go/pkg/mod
ENV PATH="/usr/local/go/bin:/go/bin:${PATH}"
ENV GOPATH=/go
ENV GOCACHE=/go/cache

# Install Claude Code CLI globally (root-owned, world-readable).
RUN npm install -g @anthropic-ai/claude-code \
    && npm cache clean --force

# Kenny's working directory. Source is baked in at build time (including
# .git) so Claude Code can read, edit, and commit from inside. The entrypoint
# refreshes the origin URL at startup to embed GITHUB_PAT for pushes.
WORKDIR /app
COPY --from=builder /src /app
COPY --from=builder /out/kenny /usr/local/bin/kenny

# Everything Kenny (and Claude Code running inside Kenny) writes to is
# owned by the node user (uid 1000) that this image ships with. /state
# is a volume mount so its ownership is re-applied at entrypoint.
RUN mkdir -p "$GOPATH" "$GOCACHE" /go/bin /state \
    && chown -R node:node /app "$GOPATH" /state

VOLUME ["/state"]

COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

EXPOSE 8080

# Entrypoint runs briefly as root to fix /state ownership on each boot
# (a volume mount masks the Dockerfile chown), then gosu-drops to the
# node user. Claude Code refuses --dangerously-skip-permissions as root.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["kenny"]
