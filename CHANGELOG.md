# Changelog

All notable changes per release. Versions follow [semantic versioning](https://semver.org/); while the major version is 0, a minor bump may contain breaking changes — they are listed under **Upgrade notes**.

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
