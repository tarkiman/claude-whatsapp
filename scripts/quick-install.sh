#!/usr/bin/env bash
# Installer satu-baris: mengunduh rilis claude-whatsapp siap pakai untuk
# arsitektur mesin ini (tanpa Go, tanpa build), mengekstraknya ke
# ~/claude-whatsapp, lalu menjalankan scripts/install.sh.
#
# Pemakaian:
#   curl -sSL https://raw.githubusercontent.com/tarkiman/claude-whatsapp/main/scripts/quick-install.sh | bash
#
# Opsi (lewat "--" setelah nama script, pola standar curl-pipe-ke-shell):
#   curl -sSL .../quick-install.sh | bash -s -- --allowed-senders 6281234567890
#
#   --dir <path>       lokasi instalasi (default ~/claude-whatsapp)
#   --version <tag>    pasang versi tertentu (default: rilis terbaru), mis. v0.1.0
#   --tarball <file>   pakai tarball lokal alih-alih unduh dari GitHub
#   sisanya            diteruskan ke scripts/install.sh (lihat install.sh --help):
#                      --allowed-senders, --gowa-port, --non-interactive, --skip-start
#
# Menjalankan ulang = upgrade: binary/script diganti, .env dan data/ dibiarkan.
# Jalankan sebagai user biasa (BUKAN sudo) — service systemd --user dan login
# 'claude' milik user itu.

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
		[ $# -ge 2 ] || die "--dir butuh nilai"
		DEST="$2"
		shift 2
		;;
	--version)
		[ $# -ge 2 ] || die "--version butuh nilai"
		VERSION="$2"
		shift 2
		;;
	--tarball)
		[ $# -ge 2 ] || die "--tarball butuh nilai"
		TARBALL="$2"
		shift 2
		;;
	*)
		INSTALL_ARGS+=("$1")
		shift
		;;
	esac
done

[ "$EUID" -ne 0 ] || die "jalankan TANPA sudo/root: curl -sSL .../quick-install.sh | bash (service systemd --user dan login 'claude' milik user biasa; kalau dijalankan sebagai root, semua file jadi milik root dan sesi berikutnya gagal permission-denied)"
[ "$(uname -s)" = "Linux" ] || die "cuma mendukung Linux (butuh systemd --user)"
command -v tar >/dev/null || die "butuh 'tar' (belum terpasang)"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

if [ -n "$TARBALL" ]; then
	[ -f "$TARBALL" ] || die "tarball tidak ditemukan: $TARBALL"
	cp "$TARBALL" "$WORKDIR/pkg.tar.gz"
else
	command -v curl >/dev/null || die "butuh 'curl' (belum terpasang) — mis. sudo apt install curl"

	case "$(uname -m)" in
	aarch64 | arm64) ARCH="arm64" ;;
	armv7l | armv6l) ARCH="armv7" ;;
	x86_64) ARCH="amd64" ;;
	*) die "arsitektur '$(uname -m)' tidak punya binary siap pakai — build dari source sesuai README" ;;
	esac
	log "Arsitektur terdeteksi: $ARCH"

	if [ -n "$VERSION" ]; then
		API_URL="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
	else
		API_URL="https://api.github.com/repos/${REPO}/releases/latest"
	fi
	log "Mencari rilis ${VERSION:-terbaru}..."
	RELEASE_JSON="$(curl -sSL "$API_URL")"
	DOWNLOAD_URL="$(echo "$RELEASE_JSON" | grep -o "\"browser_download_url\": *\"[^\"]*claude-whatsapp-[^\"]*-${ARCH}\.tar\.gz\"" | head -1 | cut -d'"' -f4)"
	[ -n "$DOWNLOAD_URL" ] || die "tidak menemukan rilis untuk arch '$ARCH' di https://github.com/${REPO}/releases — repo harus publik dan sudah punya rilis; atau pasang manual sesuai README"
	log "Unduh: $DOWNLOAD_URL"
	curl -sSL "$DOWNLOAD_URL" -o "$WORKDIR/pkg.tar.gz"
fi

tar -xzf "$WORKDIR/pkg.tar.gz" -C "$WORKDIR"
EXTRACTED_DIR="$(find "$WORKDIR" -maxdepth 1 -mindepth 1 -type d | head -1)"
[ -n "$EXTRACTED_DIR" ] && [ -x "$EXTRACTED_DIR/scripts/install.sh" ] || die "isi rilis tidak seperti yang diharapkan (scripts/install.sh tidak ada) — laporkan ini sebagai bug"

NEW_VERSION="$(cat "$EXTRACTED_DIR/VERSION" 2>/dev/null || echo "?")"
if [ -f "$DEST/VERSION" ]; then
	log "Upgrade: $(cat "$DEST/VERSION") -> $NEW_VERSION (.env dan data/ tidak diubah)"
else
	log "Memasang $NEW_VERSION ke $DEST"
fi

mkdir -p "$DEST"
cp -a "$EXTRACTED_DIR"/. "$DEST"/
cd "$DEST"

# Saat script ini dijalankan sebagai `curl | bash`, stdin bash adalah pipe
# dari curl yang sudah habis, bukan terminal — install.sh menanyakan nomor
# pengirim secara interaktif, jadi stdin harus disambungkan ke terminal
# sungguhan. Kalau tidak ada /dev/tty (mis. CI), install.sh mewarisi stdin
# apa adanya dan meminta nilainya lewat flag.
if ( exec 3</dev/tty ) 2>/dev/null; then
	scripts/install.sh ${INSTALL_ARGS[@]+"${INSTALL_ARGS[@]}"} </dev/tty
else
	scripts/install.sh ${INSTALL_ARGS[@]+"${INSTALL_ARGS[@]}"}
fi
