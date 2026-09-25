#!/usr/bin/env bash
# One-line installer: downloads a prebuilt claude-whatsapp release for this
# machine's architecture (no Go, no build), extracts it to ~/claude-whatsapp,
# then runs scripts/install.sh.
#
# Usage:
#   curl -sSL https://raw.githubusercontent.com/tarkiman/claude-whatsapp/main/scripts/quick-install.sh | bash
#
# Options (after "--", the standard curl-pipe-to-shell pattern):
#   curl -sSL .../quick-install.sh | bash -s -- --allowed-senders 6281234567890
#
#   --dir <path>       install location (default ~/claude-whatsapp)
#   --version <tag>    install a specific version (default: latest release), e.g. v0.1.0
#   --tarball <file>   use a local tarball instead of downloading from GitHub
#   anything else      is passed on to scripts/install.sh (see install.sh --help):
#                      --allowed-senders, --gowa-port, --non-interactive, --skip-start
#
# Running it again = upgrade: binaries/scripts are replaced, .env and data/
# are left alone. Run as a regular user (NOT sudo) — the systemd --user
# services and the 'claude' login belong to that user.

set -euo pipefail

REPO="tarkiman/claude-whatsapp"
DEST="$HOME/claude-whatsapp"
VERSION=""
TARBALL=""
INSTALL_ARGS=()

log() { echo "==> $*"; }
die() {
	echo "ERROR: $*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--dir)
		[ $# -ge 2 ] || die "--dir needs a value"
		DEST="$2"
		shift 2
		;;
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		VERSION="$2"
		shift 2
		;;
	--tarball)
		[ $# -ge 2 ] || die "--tarball needs a value"
		TARBALL="$2"
		shift 2
		;;
	*)
		INSTALL_ARGS+=("$1")
		shift
		;;
	esac
done

[ "$EUID" -ne 0 ] || die "run WITHOUT sudo/root: curl -sSL .../quick-install.sh | bash (the systemd --user services and the 'claude' login belong to a regular user; run as root, every file becomes root-owned and later sessions fail with permission denied)"
[ "$(uname -s)" = "Linux" ] || die "Linux only (needs systemd --user)"
command -v tar >/dev/null || die "'tar' is required (not installed)"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

if [ -n "$TARBALL" ]; then
	[ -f "$TARBALL" ] || die "tarball not found: $TARBALL"
	cp "$TARBALL" "$WORKDIR/pkg.tar.gz"
else
	command -v curl >/dev/null || die "'curl' is required (not installed) — e.g. sudo apt install curl"

	case "$(uname -m)" in
	aarch64 | arm64) ARCH="arm64" ;;
	armv7l | armv6l) ARCH="armv7" ;;
	x86_64) ARCH="amd64" ;;
	*) die "architecture '$(uname -m)' has no prebuilt binary — build from source, see the README" ;;
	esac
	log "Detected architecture: $ARCH"

	if [ -n "$VERSION" ]; then
		API_URL="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
	else
		API_URL="https://api.github.com/repos/${REPO}/releases/latest"
	fi
	log "Looking up release ${VERSION:-latest}..."
	RELEASE_JSON="$(curl -sSL "$API_URL")"
	DOWNLOAD_URL="$(echo "$RELEASE_JSON" | grep -o "\"browser_download_url\": *\"[^\"]*claude-whatsapp-[^\"]*-${ARCH}\.tar\.gz\"" | head -1 | cut -d'"' -f4)"
	[ -n "$DOWNLOAD_URL" ] || die "could not find a release for arch '$ARCH' at https://github.com/${REPO}/releases — the repo must be public and have a release; or install manually, see the README"
	log "Downloading: $DOWNLOAD_URL"
	curl -sSL "$DOWNLOAD_URL" -o "$WORKDIR/pkg.tar.gz"
fi

tar -xzf "$WORKDIR/pkg.tar.gz" -C "$WORKDIR"
EXTRACTED_DIR="$(find "$WORKDIR" -maxdepth 1 -mindepth 1 -type d | head -1)"
[ -n "$EXTRACTED_DIR" ] && [ -x "$EXTRACTED_DIR/scripts/install.sh" ] || die "release contents are not what was expected (scripts/install.sh missing) — please report this as a bug"

NEW_VERSION="$(cat "$EXTRACTED_DIR/VERSION" 2>/dev/null || echo "?")"
if [ -f "$DEST/VERSION" ]; then
	log "Upgrade: $(cat "$DEST/VERSION") -> $NEW_VERSION (.env and data/ are left untouched)"
else
	log "Installing $NEW_VERSION to $DEST"
fi

mkdir -p "$DEST"
cp -a "$EXTRACTED_DIR"/. "$DEST"/
cd "$DEST"

# When this script itself runs as `curl ... | bash`, bash's stdin is the
# (already drained) pipe from curl, not the terminal — install.sh asks for
# the sender number interactively, so stdin has to be reconnected to the real
# terminal. With no /dev/tty (e.g. CI), install.sh inherits stdin as-is and
# needs the values as flags instead.
if ( exec 3</dev/tty ) 2>/dev/null; then
	scripts/install.sh ${INSTALL_ARGS[@]+"${INSTALL_ARGS[@]}"} </dev/tty
else
	scripts/install.sh ${INSTALL_ARGS[@]+"${INSTALL_ARGS[@]}"}
fi
