#!/usr/bin/env bash
# Builds the bridge binary and (re)installs the systemd --user service,
# generating the unit from deploy/claude-whatsapp.service.template with
# this machine's actual repo path and PATH baked in — so it works
# regardless of username or where the repo is checked out.
#
# Run manually after pulling changes; does NOT touch the gowa container
# (use `docker compose up -d` for that) or .env (edit it by hand).
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_DIR="$(pwd)"

if [ ! -f .env ]; then
	echo "Missing .env — copy .env.example to .env and fill it in first." >&2
	exit 1
fi

if ! command -v claude >/dev/null 2>&1; then
	echo "Warning: 'claude' not found in your current PATH ($PATH)." >&2
	echo "The bridge will fail to run Claude until it is installed and on PATH." >&2
fi

echo "Building bridge..."
mkdir -p bin
go build -o bin/bridge ./cmd/bridge

echo "Installing systemd --user unit..."
mkdir -p "$HOME/.config/systemd/user"
sed \
	-e "s|__REPO_DIR__|$REPO_DIR|g" \
	-e "s|__PATH__|$PATH|g" \
	deploy/claude-whatsapp.service.template >"$HOME/.config/systemd/user/claude-whatsapp.service"

export XDG_RUNTIME_DIR="/run/user/$(id -u)"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
systemctl --user daemon-reload
systemctl --user enable claude-whatsapp.service
# `enable --now` only starts the unit if it wasn't already running — it does
# NOT restart it, so a rebuilt binary from a re-run of this script would
# silently keep serving the old process. Always restart explicitly.
systemctl --user restart claude-whatsapp.service
loginctl enable-linger "$(whoami)"

echo "Done. Check status with: systemctl --user status claude-whatsapp.service"
