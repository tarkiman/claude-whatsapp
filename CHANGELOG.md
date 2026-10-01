# Changelog

All notable changes per release. Versions follow [semantic versioning](https://semver.org/); while the major version is 0, a minor bump may contain breaking changes — they are listed under **Upgrade notes**.

## [0.4.3] — 2026-10-02

Reported confusing on a real install on a new set-top box: the installer downloaded the full release, printed an "Upgrade" message, and only then failed on a missing Claude CLI — wasting bandwidth and showing an "Upgrade" line right before a failure whose own message claimed "nothing was changed".

### Changed

- `quick-install.sh` now checks every prerequisite (Docker, the Compose plugin, Docker daemon access, the `claude` CLI, systemd) **before downloading the release tarball at all**, using the exact same checklist as `install.sh` (now shared from `scripts/preflight.sh`). A missing prerequisite is reported immediately, with nothing downloaded and nothing changed — matching what the message has always said. If the check can't be fetched ahead of time (an older pinned release, or a transient network hiccup), it's skipped silently and `install.sh`'s own check after extraction still catches it, exactly as before.

## [0.4.2] — 2026-10-02

Fixed right after a user asked whether a recurring "degraded" Discord alert ("1 message(s) waiting in the pending queue") was safe — it was, but the check behind it wasn't looking at how long the message had actually been there.

### Fixed

- The pending-message queue (where an inbound message sits while `claude -p` is still working on a reply — normal, by-design, and often taking several minutes) no longer flips the dashboard to "degraded" or fires a Discord alert just because it's non-empty. Both now only flag it once the oldest pending message has been stuck for over 25 minutes — comfortably past the ~20-minute worst case for a single reply (10-minute timeout, retried once) — and the dashboard shows how long the oldest message has actually been waiting instead of just a raw count.

## [0.4.1] — 2026-10-01

Discord alerts, reworked for readability right after shipping 0.4.0 — the plain-text version looked like a wall of text next to a real outage's worth of reminders.

### Changed

- Alerts are now proper Discord embeds instead of plain text: color-coded by severity (red/yellow/green), one field per component (bridge, WhatsApp, Claude) so you can tell what broke at a glance, and a native embed timestamp (Discord renders it localized, with a relative-time hover) instead of a hand-formatted date in the message body.
- Recovery messages and the 30-minute reminder now state the real elapsed time (`9h 7m`, `1d 2h`) instead of a vague "still ongoing" — tracked across intermediate severity changes, so `degraded -> down -> degraded -> ok` still reports the total outage, not just the last leg.
- *Send test alert* now sends a distinctly-colored, clearly-labeled test embed instead of a plain sentence, so it cannot be mistaken for a real alert.

## [0.4.0] — 2026-10-01

Built after a real outage (Docker's DNS couldn't reach `8.8.8.8`/`1.1.1.1`, so gowa lost WhatsApp) went unnoticed until someone asked.

### Added

- **Discord alerts.** A new "Discord alerts" card in the Admin UI sends a Discord message when the overall status changes — `ok -> degraded/down` and back to `ok` — with a reminder every 30 minutes while a problem continues. Checks run once a minute from the admin process itself (not the bridge), using the exact same status computation the dashboard shows, so alerting keeps working even when the bridge is the thing that is down.
  - *Send test alert* posts immediately, independent of the on/off setting, to confirm a webhook actually works.
  - The webhook URL is stored only in `~/.claude-whatsapp/alerts.json` (`ALERTS_FILE`, mode `0600`) - never in `.env`, never shown back in full once saved - and is validated to be a real `https://discord.com/api/webhooks/...` (or `discordapp.com`) URL before it is accepted.

## [0.3.2] — 2026-09-29

Found on the second attempt on the same set-top box: Claude had been installed while logged in as `root`, so the regular user running the installer could not see it.

### Fixed

- The installer now uses a `claude` found in `~/.local/bin` even when that directory is not on `PATH` (the official Claude installer puts the CLI there but does not add it to the current shell), and the directory ends up in the systemd units' `PATH`, so the bridge finds it as well.
- The "Claude CLI missing" message now says to install it **as the same regular user** that runs the installer (not root / `sudo`), and shows the exact line that puts `~/.local/bin` on your `PATH`. README (EN/ID) troubleshooting has a row for it.

## [0.3.1] — 2026-09-29

Found by installing from scratch on a fresh ARM set-top box (Docker present, Claude CLI not).

### Fixed

- **The installer now checks every prerequisite first** (Docker, the Compose plugin, access to the Docker daemon, the `claude` CLI on `PATH`, systemd) — before asking anything and before writing any file — and reports **all** missing pieces at once with the command to fix each. Before, it asked its questions, created `.env`, and only then stopped at the first missing piece, one per attempt. A failed run now changes nothing. `--skip-start` still skips the check.
- The Claude CLI hint used to mention only `npm`, which is useless on a machine without Node.js; it now also shows the official installer (`curl -fsSL https://claude.ai/install.sh | bash`) and how to make `claude` visible in a new shell.
- The sender number typed during an interactive install is echoed back for confirmation (a wrong country code such as `685…` for `6285…` was accepted silently). When several numbers are given, the installer says that personal mode obeys exactly one and which one it uses.

## [0.3.0] — 2026-09-28

### Upgrade notes (read before upgrading)

- **The Admin UI now has a real login** (username + password) instead of `ADMIN_PASSWORD` in `.env`. Coming from **0.2.0**: your `ADMIN_PASSWORD` is adopted automatically on the first start — sign in as `admin` with it, change it in the new *Admin login* card, then delete the line from `.env`. Coming from **0.1.x** (no password): other machines are refused until an account exists — open the page on the machine itself and create it there, or run `bin/admin passwd`.
- **HTTP Basic authentication is no longer accepted.** The page uses a login form and a session cookie.
- `scripts/deploy.sh` no longer generates a random `ADMIN_PASSWORD`; the installer asks for the login instead. `deploy.sh` only prints a notice when the admin listens beyond loopback and no login exists yet.
- The admin no longer refuses to start when it listens beyond loopback without `ADMIN_PASSWORD`; instead it refuses every other machine until an account exists.

### Added

- **Login for the Admin UI**: one account whose password is stored only as a salted PBKDF2-SHA256 hash (600,000 iterations, standard library) in `~/.claude-whatsapp/admin.json` (`ADMIN_AUTH_FILE`, mode `0600`) — never in `.env`.
  - login page, session cookie (`HttpOnly`, `SameSite=Strict`, `Secure` behind TLS), sessions expire after 30 minutes idle / 12 hours;
  - **Change password** card (asks for the current password, signs every other browser out) and **Log out**;
  - password rules stated up front (at least 10 characters with at least 5 different ones; no digits/symbols demanded, a short phrase works) and a refusal lists every rule it broke, with the numbers;
  - guess throttling: 5 wrong passwords lock a client out for 5 minutes, doubling each time (up to an hour);
  - first-time setup page that only works from the machine itself and only while no account exists;
  - `bin/admin passwd [--user NAME] [--stdin]` to create or reset the login from a terminal — the recovery path for a forgotten password;
  - the installer asks for the admin username and password (without echo); `--admin-user`, and `ADMIN_PASSWORD` in the installer's environment for non-interactive installs (never a flag).
- Migration: a legacy `ADMIN_PASSWORD` is hashed into the login file once, then ignored (`ADMIN_USER` picks the username, default `admin`).
- Tests for the hash store, sessions, limiter, every login/setup/logout/change-password path, the `passwd` command, and the whole flow driven in a real browser; the authentication rules are mutation-tested.

### Security

- The admin password is no longer stored in clear text anywhere; a password changed from the UI or with `bin/admin passwd` invalidates every existing session, even when done while the admin is running.
- Login verification takes the same time whether or not the username exists, and wrong-password answers never say which part was wrong.

## [0.2.0] — 2026-09-28

### Upgrade notes (read before upgrading from 0.1.x)

- **`ALLOWED_GROUPS` no longer admits anybody.** In 0.1.x, listing a group let *every* member instruct the bot. Groups are now **team mode**: one group, an approved-member roster and a required @mention, configured in the Admin UI. The bridge logs a warning if `ALLOWED_GROUPS` is still set.
- **Personal mode has exactly one number.** The first entry of `ALLOWED_SENDERS` becomes the owner; extra entries are ignored (with a warning in the log). Direct messages from that number behave exactly as before. Once you save a policy in the Admin UI it lives in `access.json` and `ALLOWED_SENDERS` is no longer consulted.
- **`ADMIN_PASSWORD` is required whenever the Admin UI listens beyond loopback** (e.g. a LAN or ZeroTier address in `ADMIN_ADDR`), because that page now decides who may run commands on the machine. `scripts/deploy.sh` (and therefore the installer and upgrades) generates one when it is missing and tells you where to read it: `grep ADMIN_PASSWORD .env`. Your browser will ask for user `admin`.
- Sender matching is now **exact** (see *Security* below). A bare number in `ALLOWED_SENDERS` no longer matches longer numbers that merely start with the same digits.

### Added

- **Personal and team access modes**, managed from a new *Who can instruct the bot* card in the Admin UI, applied without a restart:
  - *personal* — exactly one phone number, DMs only, groups ignored;
  - *team* — one WhatsApp group, only approved members, only when they @mention the bot; members added to the group later are denied until ticked; the answer is posted to the group quoting the request; one Claude session per group, and the prompt says who is speaking.
- Group member roster in the Admin UI (name, number, admin flag, "the bot"), fed by gowa's `GET /group/participants`.
- @mention detection that accepts the bot's phone number or its WhatsApp LID (the LID is learned from the group's member list and cached).
- `ACCESS_FILE` (default `~/.claude-whatsapp/access.json`) and `LOG_GROUP_MESSAGES=1` (logs group message text and the decision, to diagnose mention detection).
- `scripts/deploy.sh` creates `ADMIN_PASSWORD` when an existing setup needs one (see upgrade notes).
- Tests: decision tables for both modes, end-to-end tests that push real HMAC-signed webhooks through the handler against a fake `claude` and a fake gowa, admin API tests; the security rules are mutation-tested.
- This changelog; `README.md` / `docs/ARCHITECTURE.md` describe both modes (English and Indonesian).

### Security

- Sender matching is exact on the JID's user and server parts. Fixed: a bare number acted as a prefix wildcard, and an `@lid` identifier whose digits started with an allowed number matched it.
- The pending-queue replay after a restart re-checks authorisation, so a message queued before its sender was removed or un-approved is dropped instead of executed.
- A missing/invalid `access.json` denies everybody instead of falling back to a more permissive setting.
- WhatsApp display names (chosen by the sender) are sanitised before they reach a prompt and are only ever rendered as text in the Admin UI.

### Fixed

- The sequence diagram in `docs/ARCHITECTURE*.md` did not render on GitHub (a `;` inside a mermaid note).

## [0.1.1] — 2026-09-26

- `docs/ARCHITECTURE.md` is now English, with the Indonesian original as `docs/ARCHITECTURE.id.md`; both READMEs and the release tarball link/ship both.

## [0.1.0] — 2026-09-26

First public release.

- WhatsApp ↔ Claude Code bridge on top of gowa: text, attachments (images, video, documents, stickers) and voice notes (local `whisper.cpp`), one resumable Claude session per chat, durable on-disk queue replayed after a crash, one `claude -p` at a time per chat.
- Admin UI (loopback by default, optional LAN/ZeroTier with a client-network allowlist): status dashboard, WhatsApp recovery (reconnect, pairing by QR or code, unlink) and Claude sign-in.
- One-line installer (`curl | bash`), prebuilt arm64 / armv7 / amd64 tarballs, systemd `--user` services.
- MIT license; English and Indonesian README.
