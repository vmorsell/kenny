#!/bin/sh
set -eu

# Configure git identity and credentials so Claude Code can commit + push
# from inside the container. No-ops for fields the user hasn't set.

GIT_USER_NAME="${GIT_USER_NAME:-Kenny}"
GIT_USER_EMAIL="${GIT_USER_EMAIL:-kenny@local}"
GITHUB_REPO="${GITHUB_REPO:-}"

if [ -d /app/.git ]; then
    git -C /app config user.name "$GIT_USER_NAME"
    git -C /app config user.email "$GIT_USER_EMAIL"
    git -C /app config --add safe.directory /app

    if [ -n "${GITHUB_PAT:-}" ] && [ -n "$GITHUB_REPO" ]; then
        git -C /app remote set-url origin \
            "https://x-access-token:${GITHUB_PAT}@github.com/${GITHUB_REPO}.git"
    fi
fi

exec "$@"
