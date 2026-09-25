#!/usr/bin/env bash
# Sets up and starts claude-whatsapp from this directory (an extracted
# release, or a repo checkout): creates .env with random secrets, starts gowa
# with Docker Compose, then installs the systemd --user services (bridge +
# admin UI). Pairing WhatsApp and signing in to Claude are done afterwards
# from the admin UI — not here.
#
# Normally called by scripts/quick-install.sh; safe to re-run (upgrade): an
# existing .env is NOT modified.
#
# Usage:
#   scripts/install.sh [--allowed-senders <number[,number…]>] [--gowa-port <port>]
#                      [--non-interactive] [--skip-start]
#
#   --allowed-senders  your phone number (the sender) with country code, e.g.
#                      6281234567890 or 6281234567890@s.whatsapp.net.
#                      Asked interactively if not given.
#   --gowa-port        gowa's port on the host (default 3011)
#   --non-interactive  never prompt; fail if a required value is missing
#   --skip-start       only prepare .env — don't start Docker/the services
set -euo pipefail
cd "$(dirname "$0")/.."

log() { echo "==> $*"; }
warn() { echo "WARNING: $*" >&2; }
die() {
	echo "ERROR: $*" >&2
	exit 1
}

ALLOWED_SENDERS_ARG=""
GOWA_PORT_ARG=""
INTERACTIVE=1
SKIP_START=0

while [ $# -gt 0 ]; do
	case "$1" in
	--allowed-senders)
		[ $# -ge 2 ] || die "--allowed-senders needs a value"
		ALLOWED_SENDERS_ARG="$2"
		shift 2
		;;
	--gowa-port)
		[ $# -ge 2 ] || die "--gowa-port needs a value"
		GOWA_PORT_ARG="$2"
		shift 2
		;;
	--non-interactive)
		INTERACTIVE=0
		shift
		;;
	--skip-start)
		SKIP_START=1
		shift
		;;
	-h | --help)
		sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*) die "unknown argument: $1 (see --help)" ;;
	esac
done

[ "$EUID" -ne 0 ] || die "run as a regular user, not root/sudo — the systemd --user services and the 'claude' login belong to that user"

# ask reads from the real terminal even when this script's stdin is a pipe
# (curl | bash), same trick scripts/quick-install.sh uses.
ask() {
	local prompt="$1" answer=""
	if [ -t 0 ]; then
		read -r -p "$prompt" answer
	elif ( exec 3</dev/tty ) 2>/dev/null; then
		read -r -p "$prompt" answer </dev/tty
	else
		die "no terminal to ask on — pass the value as a flag (e.g. --allowed-senders) or use --non-interactive"
	fi
	printf '%s' "$answer"
}

random_hex() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -hex "$1"
	else
		head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'
	fi
}

# Writes KEY=VALUE into .env, replacing every existing line for KEY (the
# gowa password appears twice in .env.example) or appending if absent.
set_env() {
	local key="$1" raw="$2" v="$2"
	v="${v//\\/\\\\}"
	v="${v//&/\\&}"
	v="${v//|/\\|}"
	if grep -q "^${key}=" .env; then
		sed -i "s|^${key}=.*|${key}=${v}|" .env
	else
		printf '%s=%s\n' "$key" "$raw" >>.env
	fi
}

# 6281234567890 / +62 812-3456-7890 / 6281…@s.whatsapp.net -> JID list
normalize_senders() {
	local out="" item digits
	IFS=',' read -ra items <<<"$1"
	for item in "${items[@]}"; do
		item="$(echo "$item" | tr -d '[:space:]')"
		[ -n "$item" ] || continue
		if [[ "$item" == *@* ]]; then
			out+="${out:+,}$item"
			continue
		fi
		digits="$(echo "$item" | tr -cd '0-9')"
		[ -n "$digits" ] || die "invalid number: '$item'"
		[[ "$digits" != 0* ]] || die "number '$item' starts with 0 — use the country code (e.g. 62812… not 0812…)"
		[ "${#digits}" -ge 8 ] || die "number '$item' is too short — include the country code"
		out+="${out:+,}${digits}@s.whatsapp.net"
	done
	[ -n "$out" ] || die "ALLOWED_SENDERS is empty"
	printf '%s' "$out"
}

# --- .env ------------------------------------------------------------------
if [ -f .env ]; then
	log ".env already exists — keeping it as is (not modified)."
else
	[ -f .env.example ] || die ".env.example not found — run this from an extracted release / repo checkout"
	senders="$ALLOWED_SENDERS_ARG"
	if [ -z "$senders" ]; then
		[ "$INTERACTIVE" -eq 1 ] || die "--allowed-senders is required together with --non-interactive"
		echo
		echo "WhatsApp number allowed to chat with the bot (YOUR OWN phone number, with country"
		echo "code, no leading 0 — e.g. 6281234567890). Separate several with commas."
		senders="$(ask "Sender number: ")"
	fi
	senders="$(normalize_senders "$senders")"

	log "Creating .env with random secrets..."
	cp .env.example .env
	chmod 600 .env
	pass="$(random_hex 12)"
	set_env WEBHOOK_SECRET "$(random_hex 32)"
	set_env GOWA_BASIC_AUTH_PASSWORD "$pass"
	set_env ALLOWED_SENDERS "$senders"
	if [ -n "$GOWA_PORT_ARG" ]; then
		set_env GOWA_PORT "$GOWA_PORT_ARG"
		set_env GOWA_BASE_URL "http://localhost:$GOWA_PORT_ARG"
	fi
	log "ALLOWED_SENDERS=$senders"
fi

if [ "$SKIP_START" -eq 1 ]; then
	log "--skip-start: .env is ready. Continue yourself: docker compose up -d && scripts/deploy.sh"
	exit 0
fi

# --- prerequisites ------------------------------------------------------------
command -v docker >/dev/null 2>&1 || die "Docker is not installed — https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "the Docker Compose plugin is not installed ('docker compose version' failed)"
docker info >/dev/null 2>&1 || die "cannot talk to the Docker daemon — make sure it is running and this user is in the docker group (sudo usermod -aG docker \$USER, then log in again)"
command -v claude >/dev/null 2>&1 || die "Claude Code CLI ('claude') is not on your PATH — npm install -g @anthropic-ai/claude-code (see the README)"
command -v systemctl >/dev/null 2>&1 || die "systemd not found — the bridge is installed as a systemd --user service"

# --- gowa + services ------------------------------------------------------------
log "Starting gowa (Docker Compose)..."
docker compose up -d

log "Installing the bridge + admin services (systemd --user)..."
scripts/deploy.sh

# --- wait for admin, then tell the user what is left ---------------------------
admin_url="http://127.0.0.1:8098"
for _ in $(seq 1 20); do
	if curl -fsS -o /dev/null "$admin_url/api/status" 2>/dev/null; then
		up=1
		break
	fi
	sleep 1
done
[ "${up:-0}" -eq 1 ] || warn "the admin page is not answering yet — check: systemctl --user status claude-whatsapp-admin.service"

host_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
claude_state="not signed in"
if claude auth status 2>/dev/null | grep -q '"loggedIn": *true'; then
	claude_state="signed in"
fi

cat <<EOF

==> Installed. Two steps left, both from the admin page:

  1. Open $admin_url
     (from another computer: ssh -L 8098:127.0.0.1:8098 $(whoami)@${host_ip:-<host-ip>}
      then open http://localhost:8098 — or follow the LAN/ZeroTier notes in .env.example)

  2. "WhatsApp recovery" card  → Show pairing QR (or Get code) → link WhatsApp
     "Sign in / switch Claude account" card → Claude account: $claude_state
     (if "not signed in", click Sign in on that card)

  Then send a WhatsApp message to the newly linked number from the sender number above.
  Status and logs are on the same admin page at any time.
EOF
