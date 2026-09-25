#!/usr/bin/env bash
# Menyiapkan & menjalankan claude-whatsapp dari direktori ini (hasil ekstrak
# rilis, atau checkout repo): membuat .env dengan secret acak, menjalankan
# gowa via Docker Compose, lalu memasang service systemd --user (bridge +
# admin UI). Pairing WhatsApp dan login akun Claude dilakukan sesudahnya dari
# halaman admin — bukan di sini.
#
# Biasanya dipanggil oleh scripts/quick-install.sh; aman dijalankan ulang
# (upgrade): .env yang sudah ada TIDAK diubah.
#
# Pemakaian:
#   scripts/install.sh [--allowed-senders <nomor[,nomor…]>] [--gowa-port <port>]
#                      [--non-interactive] [--skip-start]
#
#   --allowed-senders  nomor HP Anda (pengirim) dengan kode negara, mis.
#                      6281234567890 atau 6281234567890@s.whatsapp.net.
#                      Ditanyakan interaktif kalau tidak diberikan.
#   --gowa-port        port gowa di host (default 3011)
#   --non-interactive  jangan bertanya apa pun; gagal kalau ada yang wajib kosong
#   --skip-start       cuma siapkan .env — jangan jalankan Docker/service
set -euo pipefail
cd "$(dirname "$0")/.."

log() { echo "==> $*"; }
warn() { echo "PERINGATAN: $*" >&2; }
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
		[ $# -ge 2 ] || die "--allowed-senders butuh nilai"
		ALLOWED_SENDERS_ARG="$2"
		shift 2
		;;
	--gowa-port)
		[ $# -ge 2 ] || die "--gowa-port butuh nilai"
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
	*) die "argumen tidak dikenal: $1 (lihat --help)" ;;
	esac
done

[ "$EUID" -ne 0 ] || die "jalankan sebagai user biasa, bukan root/sudo — service systemd --user dan login 'claude' milik user itu"

# ask reads from the real terminal even when this script's stdin is a pipe
# (curl | bash), same trick scripts/quick-install.sh uses.
ask() {
	local prompt="$1" answer=""
	if [ -t 0 ]; then
		read -r -p "$prompt" answer
	elif ( exec 3</dev/tty ) 2>/dev/null; then
		read -r -p "$prompt" answer </dev/tty
	else
		die "tidak ada terminal untuk bertanya — beri nilainya lewat flag (mis. --allowed-senders) atau pakai --non-interactive"
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
		[ -n "$digits" ] || die "nomor tidak valid: '$item'"
		[[ "$digits" != 0* ]] || die "nomor '$item' diawali 0 — pakai kode negara (mis. 62812… bukan 0812…)"
		[ "${#digits}" -ge 8 ] || die "nomor '$item' terlalu pendek — sertakan kode negara"
		out+="${out:+,}${digits}@s.whatsapp.net"
	done
	[ -n "$out" ] || die "ALLOWED_SENDERS kosong"
	printf '%s' "$out"
}

# --- .env ------------------------------------------------------------------
if [ -f .env ]; then
	log ".env sudah ada — dipakai apa adanya (tidak diubah)."
else
	[ -f .env.example ] || die ".env.example tidak ditemukan — jalankan dari direktori hasil ekstrak rilis / checkout repo"
	senders="$ALLOWED_SENDERS_ARG"
	if [ -z "$senders" ]; then
		[ "$INTERACTIVE" -eq 1 ] || die "--allowed-senders wajib diisi bersama --non-interactive"
		echo
		echo "Nomor WhatsApp yang boleh chat ke bot (nomor HP ANDA sendiri, pakai kode negara,"
		echo "tanpa 0 di depan — mis. 6281234567890). Pisahkan dengan koma kalau lebih dari satu."
		senders="$(ask "Nomor pengirim: ")"
	fi
	senders="$(normalize_senders "$senders")"

	log "Membuat .env dengan secret acak..."
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
	log "--skip-start: .env siap. Lanjutkan sendiri: docker compose up -d && scripts/deploy.sh"
	exit 0
fi

# --- prerequisites ------------------------------------------------------------
command -v docker >/dev/null 2>&1 || die "Docker belum terpasang — https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "plugin Docker Compose belum terpasang ('docker compose version' gagal)"
docker info >/dev/null 2>&1 || die "tidak bisa bicara ke Docker daemon — pastikan daemon jalan dan user ini ada di grup docker (sudo usermod -aG docker \$USER, lalu login ulang)"
command -v claude >/dev/null 2>&1 || die "Claude Code CLI ('claude') belum ada di PATH — npm install -g @anthropic-ai/claude-code (lihat README)"
command -v systemctl >/dev/null 2>&1 || die "systemd tidak ditemukan — bridge dipasang sebagai service systemd --user"

# --- gowa + services ------------------------------------------------------------
log "Menjalankan gowa (Docker Compose)..."
docker compose up -d

log "Memasang service bridge + admin (systemd --user)..."
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
[ "${up:-0}" -eq 1 ] || warn "halaman admin belum menjawab — cek: systemctl --user status claude-whatsapp-admin.service"

host_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
claude_state="belum login"
if claude auth status 2>/dev/null | grep -q '"loggedIn": *true'; then
	claude_state="sudah login"
fi

cat <<EOF

==> Terpasang. Tinggal dua langkah dari halaman admin:

  1. Buka $admin_url
     (dari komputer lain: ssh -L 8098:127.0.0.1:8098 $(whoami)@${host_ip:-<ip-host>}
      lalu buka http://localhost:8098 — atau ikuti panduan LAN/ZeroTier di .env.example)

  2. Kartu "WhatsApp recovery"  → Show pairing QR (atau Get code) → tautkan WhatsApp
     Kartu "Sign in / switch Claude account" → akun Claude: $claude_state
     (kalau "belum login", klik Sign in di kartu itu)

  Lalu kirim pesan WhatsApp ke nomor yang baru ditautkan dari nomor pengirim di atas.
  Status/log kapan saja: halaman admin yang sama.
EOF
