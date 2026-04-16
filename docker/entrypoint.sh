#!/bin/sh
set -eu

# Ensure the node user can write to /state and /app after a volume mount.
# Volume mounts clobber whatever chown was baked into the image.
chown -R node:node /state /app 2>/dev/null || true

# Configure git identity + credentials as the node user so the .gitconfig
# and credentials land in /home/node.
GIT_USER_NAME="${GIT_USER_NAME:-Kenny}"
GIT_USER_EMAIL="${GIT_USER_EMAIL:-kenny@local}"
GITHUB_REPO="${GITHUB_REPO:-}"

if [ -d /app/.git ]; then
    gosu node git -C /app config user.name "$GIT_USER_NAME"
    gosu node git -C /app config user.email "$GIT_USER_EMAIL"
    gosu node git -C /app config --add safe.directory /app

    if [ -n "${GITHUB_PAT:-}" ] && [ -n "$GITHUB_REPO" ]; then
        gosu node git -C /app remote set-url origin \
            "https://x-access-token:${GITHUB_PAT}@github.com/${GITHUB_REPO}.git"
    fi
fi

# Drop privileges before exec'ing Kenny. Claude Code's
# --dangerously-skip-permissions refuses to run as root.
exec gosu node "$@"
