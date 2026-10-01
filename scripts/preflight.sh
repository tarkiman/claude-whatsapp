# Shared prerequisite checks for claude-whatsapp. Meant to be SOURCED (not
# executed) by scripts/install.sh and scripts/quick-install.sh, both of which
# already set `set -euo pipefail` and define `log()` before sourcing this —
# so this file relies on both without redefining them.
#
# Checks everything the installation needs before a single question is asked
# or a file is written, and reports all that is missing at once with how to
# fix it — so a failed run leaves nothing behind and needs one retry, not one
# retry per missing piece.

# The official Claude installer puts the CLI in ~/.local/bin but does not add
# it to the current shell's PATH. Use it from there — and because
# scripts/deploy.sh bakes this PATH into the systemd units, the bridge finds
# it as well. Done at source time (not inside preflight()) so it also takes
# effect for quick-install.sh's early, pre-download check.
if ! command -v claude >/dev/null 2>&1 && [ -x "$HOME/.local/bin/claude" ]; then
	export PATH="$HOME/.local/bin:$PATH"
	log "Found claude in ~/.local/bin (it was not on your PATH) — using it."
fi

preflight() {
	local problems=() p n=0
	if ! command -v docker >/dev/null 2>&1; then
		problems+=("Docker is not installed.
       Install it: https://docs.docker.com/engine/install/
       then let your user use it: sudo usermod -aG docker \$USER   (and log in again)")
	else
		docker compose version >/dev/null 2>&1 || problems+=("the Docker Compose plugin is missing ('docker compose version' fails).
       Debian/Ubuntu: sudo apt-get install docker-compose-plugin")
		docker info >/dev/null 2>&1 || problems+=("cannot talk to the Docker daemon.
       Make sure it is running (sudo systemctl start docker) and that your user may use it:
       sudo usermod -aG docker \$USER   (then log in again)")
	fi
	command -v claude >/dev/null 2>&1 || problems+=("the Claude Code CLI ('claude') is not on your PATH. Install it AS THE SAME REGULAR USER
       that runs this installer (not as root or with sudo — it goes into that user's home), with either
         curl -fsSL https://claude.ai/install.sh | bash     (no Node.js needed)
         npm install -g @anthropic-ai/claude-code            (needs Node.js)
       then run this installer again. If 'claude' still is not found, add it to your PATH:
         echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bashrc && source ~/.bashrc
       You do not have to sign in now — that is done later from the admin page.")
	command -v systemctl >/dev/null 2>&1 || problems+=("systemd was not found — the bridge runs as a systemd --user service.")
	[ "${#problems[@]}" -eq 0 ] && return 0
	echo "The installation cannot start yet — ${#problems[@]} prerequisite(s) missing (nothing was changed):" >&2
	for p in "${problems[@]}"; do
		n=$((n + 1))
		printf '  %d. %s\n' "$n" "$p" >&2
	done
	echo >&2
	echo "Fix the above, then run the same command again." >&2
	exit 1
}
