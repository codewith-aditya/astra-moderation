#!/bin/sh
# Entrypoint: fix ownership of the data directory (bind mounts are created
# root-owned by Docker) and drop privileges to the unprivileged "app" user.
set -e

if [ "$(id -u)" = "0" ]; then
    if [ -n "$MOD_DATA_DIR" ] && [ -d "$MOD_DATA_DIR" ]; then
        chown -R app:app "$MOD_DATA_DIR" 2>/dev/null || true
    fi
    exec su-exec app "$@"
fi

exec "$@"
