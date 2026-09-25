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

if [ -d cmd/bridge ] && [ -d cmd/admin ] && command -v go >/dev/null 2>&1; then
	echo "Building bridge and admin..."
	mkdir -p bin
	go build -o bin/bridge ./cmd/bridge
	go build -o bin/admin ./cmd/admin
elif [ -x bin/bridge ] && [ -x bin/admin ]; then
	# Release tarball (scripts/quick-install.sh): binaries ship prebuilt, no
	# source tree and no Go toolchain.
	echo "Using prebuilt binaries in bin/ (no build)."
else
	echo "No prebuilt bin/bridge + bin/admin and no Go toolchain + source to build them." >&2
	echo "Install Go (see README) or use a release tarball." >&2
	exit 1
fi

echo "Installing systemd --user units..."
mkdir -p "$HOME/.config/systemd/user"
for unit in claude-whatsapp claude-whatsapp-admin; do
	sed \
		-e "s|__REPO_DIR__|$REPO_DIR|g" \
		-e "s|__PATH__|$PATH|g" \
		"deploy/$unit.service.template" >"$HOME/.config/systemd/user/$unit.service"
done

export XDG_RUNTIME_DIR="/run/user/$(id -u)"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
systemctl --user daemon-reload
systemctl --user enable claude-whatsapp.service claude-whatsapp-admin.service
# `enable --now` only starts the unit if it wasn't already running — it does
# NOT restart it, so a rebuilt binary from a re-run of this script would
# silently keep serving the old process. Always restart explicitly.
systemctl --user restart claude-whatsapp.service claude-whatsapp-admin.service
loginctl enable-linger "$(whoami)"

echo "Done. Check status with: systemctl --user status claude-whatsapp.service"
echo "Admin UI (loopback only): http://127.0.0.1:8098 — from another machine use"
echo "  ssh -L 8098:127.0.0.1:8098 <this-host>   then open http://localhost:8098"
