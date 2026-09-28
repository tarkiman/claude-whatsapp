# claude-whatsapp

**English** · [Bahasa Indonesia](README.id.md)

Chat with [Claude Code](https://claude.com/claude-code) over WhatsApp. Each incoming message triggers one `claude -p` call on your own machine, and the result is sent back as a WhatsApp reply — attachments (images, documents) and voice notes included. Conversations continue per chat through `claude --resume`.

![Admin dashboard](docs/images/dashboard.png)

**Contents:** [How it works](#how-it-works) · [Read this first](#read-this-first-security) · [Quick start](#quick-start) · [Who can instruct the bot](#who-can-instruct-the-bot) · [Admin UI](#admin-ui) · [Configuration](#configuration) · [Upgrade & uninstall](#upgrade--uninstall) · [Manual install](#manual-install-from-source) · [Troubleshooting](#troubleshooting) · [Features](#features)

## How it works

Built on [gowa](https://github.com/aldinokemal/go-whatsapp-web-multidevice) (an unofficial WhatsApp client, Go + [whatsmeow](https://github.com/tulir/whatsmeow)), which holds the WhatsApp connection as a Docker container, plus a small Go bridge (this repo) that connects it to the Claude Code CLI.

```mermaid
flowchart LR
    Phone["📱 Phone\n(WhatsApp)"] <-->|messages| WA[("WhatsApp\nServers")]
    WA <-->|"whatsmeow\n(linked device)"| Gowa["🐳 gowa\nDocker · :3011"]
    Gowa -->|"POST /webhook\n(HMAC-signed)"| Bridge["🌉 bridge\nGo · systemd · :8099"]
    Bridge -->|"exec subprocess"| Claude["🤖 claude CLI\n-p --resume"]
    Claude -->|"stdout JSON"| Bridge
    Bridge -->|"POST /send/message"| Gowa
```

It is deliberately as simple as possible: **no interactive process to keep alive**. gowa stands alone as a container (easy to restart), the bridge is a plain headless HTTP server (`systemd --user`, `Restart=always`), and every message is one self-contained `claude -p` call. Full details: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

Two WhatsApp numbers are involved:

| | Role | Where it is set |
|---|---|---|
| **Bot number** | The number that gets **linked** to gowa as a *linked device* — you send your messages to this number. Ideally a dedicated number, not your main one. | Pairing through the [Admin UI](#3-link-whatsapp) |
| **Sender number** | **Your** phone number, the one allowed to give commands to the bot. Messages from any other number are ignored. | Admin UI → *Who can instruct the bot* (seeded from `ALLOWED_SENDERS` in `.env`) |

## Read this first (security)

- **This is not a sandbox.** `claude -p` runs as the Linux user that installed the bridge, with `--permission-mode auto`. Anyone allowed to instruct the bot (the one number in personal mode, every approved member in team mode) can effectively make Claude read files and run commands on that machine — including `sudo` if that user has passwordless `sudo`. Only allow people you fully trust, and consider installing it under a separate unprivileged user or VM. Details in [`docs/ARCHITECTURE.md` §11](docs/ARCHITECTURE.md#11-security).
- **The access policy is the only gate — protect the accounts on it.** Anything from a number that is not allowed is ignored before anything runs (matching is exact, see [`docs/ARCHITECTURE.md` §10](docs/ARCHITECTURE.md#10-access-control-personal-and-team-modes)), and a broken policy file denies everybody. But whoever controls an allowed WhatsApp account controls the machine, so turn on WhatsApp two-step verification for it. Content an allowed person forwards (messages, documents) can also carry instructions aimed at Claude — treat forwarded content like something you are about to run yourself.
- **Unofficial WhatsApp client.** gowa/whatsmeow is not an official WhatsApp product; using it may go against WhatsApp's terms of service and can get an account restricted. Use at your own risk — preferably with a dedicated number.
- **Keep `.env` and `data/` private.** `.env` holds the webhook secret and the gowa password; `data/whatsapp/` is the live WhatsApp session (full access to the bot account). Both are in `.gitignore` — never commit or share them.
- **The Admin UI is for you only.** It can re-link WhatsApp, change the Claude login and decide who may instruct the bot. It has its own login (username + password, stored only as a hash), is reachable only from the machine itself by default, and must never be exposed to the internet. See [Admin UI](#admin-ui).

## Quick start

### 1. Prerequisites

- Linux with **systemd** (tested on a Raspberry Pi 5 / aarch64 Debian 13; also built for armv7 and x86_64)
- [Docker](https://docs.docker.com/engine/install/) with the Compose plugin, and your user in the `docker` group
- [Claude Code CLI](https://docs.claude.com/claude-code) installed **as the same regular user that runs the installer** (not as root, not with `sudo` — it goes into that user's home): `curl -fsSL https://claude.ai/install.sh | bash` (no Node.js needed) or `npm install -g @anthropic-ai/claude-code`. The installer also finds a `claude` in `~/.local/bin` that is not on your `PATH` yet. Signing in can be done after installation, from the Admin UI
- A WhatsApp number to use as the bot, and a phone to scan its QR code

Go is **not** needed for this route.

### 2. Install

Run as a **regular user, not with `sudo`**:

```bash
curl -sSL https://raw.githubusercontent.com/tarkiman/claude-whatsapp/main/scripts/quick-install.sh | bash
```

The installer downloads a prebuilt release and first **checks every prerequisite** — if anything is missing it lists it all at once, with how to fix it, and changes nothing. Then it asks for your **sender number** (your own phone number with country code and no leading 0, e.g. `6281234567890`) and for the **username and password of the admin page** (typed without echo; at least 10 characters with at least 5 different ones — a short phrase of unrelated words works well), creates `.env` with random secrets, starts gowa with Docker Compose, and installs the bridge and admin services. Everything goes into `~/claude-whatsapp`.

<details>
<summary>Installer options</summary>

```bash
curl -sSL .../quick-install.sh | bash -s -- --allowed-senders 6281234567890 --non-interactive
```

| Option | Effect |
|---|---|
| `--allowed-senders <number[,number]>` | sender number(s), without the interactive prompt |
| `--dir <path>` | install location (default `~/claude-whatsapp`) |
| `--version <tag>` | install a specific version, e.g. `v0.1.0` (default: latest release) |
| `--admin-user <name>` | username of the admin page login (otherwise asked, default `admin`). The password is asked without echo, or — when non-interactive — read from the `ADMIN_PASSWORD` *environment variable* (never a flag, so it stays out of your shell history) |
| `--gowa-port <port>` | gowa's host port (default `3011`) |
| `--tarball <file>` | use a local release tarball instead of downloading one |
| `--skip-start` | only prepare `.env`; don't start Docker or the services |
| `--non-interactive` | never prompt |

</details>

### 3. Link WhatsApp

Open the Admin UI at **http://127.0.0.1:8098** (from another computer: [SSH tunnel](#access-from-another-computer)) and sign in with the username and password you chose during installation. In the **WhatsApp recovery** card click **Show pairing QR**, then on the bot's phone go to *WhatsApp → Linked devices → Link a device* and scan the QR. The QR is valid for 30 seconds; the page shows success by itself. If you prefer a code, enter the bot's number and click **Get code**.

![The admin login page](docs/images/login.png)

![Linking WhatsApp with a QR code](docs/images/pair-whatsapp.png)

### 4. Sign in to Claude

In the **Sign in / switch Claude account** card click **Sign in…**, open the link that appears (on any device), sign in with your Claude account, then paste the code shown on that page into the "paste code" field and click **Submit code**. If the **Claude account** card at the top already says `Logged in: yes`, skip this step.

![Claude sign-in from the Admin UI](docs/images/claude-signin.png)

This login is shared by **every** Claude Code session of that Linux user, not just the bridge; the bridge uses a new login from the very next message, no restart needed. Alternative from a terminal: `claude auth login`.

### 5. Try it

From the sender number, send any WhatsApp message to the bot number. The overall status in the Admin UI should read **All good**. If nothing comes back, see [Troubleshooting](#troubleshooting).

Out of the box the bot is in **personal mode**: only your sender number can instruct it, and groups are ignored. To let a team use it in a WhatsApp group, see [Who can instruct the bot](#who-can-instruct-the-bot).

## Who can instruct the bot

Instructions become commands on your machine, so the bot has exactly two modes, both managed from the **Who can instruct the bot** card of the [Admin UI](#admin-ui). Changes apply from the very next message — no restart.

| | **Personal** (default) | **Team** |
|---|---|---|
| Who | exactly one phone number | members of one WhatsApp group whom you approved |
| Where | direct messages only; groups are ignored | that group only; DMs are ignored |
| Trigger | any message | only messages that **@mention the bot** |
| New people | — | denied until you tick them |

![Team mode in the Admin UI](docs/images/access-team.png)

**Personal mode** is what you get after installation, using the number you gave the installer. To change it, type another number in the card and save — there is only ever one.

**Team mode** — the goal is a shared agent that a whole team can see working:

1. Create a WhatsApp group and add the **bot's number** to it, plus your teammates.
2. In the Admin UI choose **Team**, press **Load groups**, pick the group. Its current members appear with a checkbox each.
3. Tick the people who may instruct the bot (or **Approve all current members**) and press **Save team**.
4. Approved members write `@bot <request>` in the group. The bot answers **in the group, quoting the request**, so everybody sees the progress. Everyone shares one Claude session per group, and the bot knows who is speaking.

Things worth knowing:

- **Approval is explicit.** Someone added to the WhatsApp group later is *not* allowed until you tick them in the card; an empty roster allows nobody. Messages that don't mention the bot are ordinary chat and are ignored, whoever writes them.
- **Every approved member is effectively an administrator of this machine** (see [security](#read-this-first-security)). For a team, run the bridge as a dedicated unprivileged user or VM, point `WORK_DIR` at a project folder instead of `$HOME`, and avoid passwordless `sudo`.
- **Privacy.** WhatsApp delivers every message of the group to the bot's account, and gowa keeps a copy of all of them (including chatter that never mentions the bot) in `data/whatsapp/chatstorage.db`. Tell your team the bot account can see the whole group.
- **The mention must be a real @mention** (pick the bot from the suggestions when typing `@`). Detection is done on the message text, accepting the bot's phone number or its WhatsApp LID. If it doesn't react, set `LOG_GROUP_MESSAGES=1` in `.env`, restart, mention the bot once and read `journalctl --user -u claude-whatsapp.service` — the `group-message:` line shows exactly what arrived and why it was accepted or ignored (it logs message text, so switch it off again).
- People WhatsApp knows only by an internal ID (no phone number visible) can't be approved; the card marks them.

## Admin UI

A status and recovery page at `http://127.0.0.1:8098`, run as its own service (`claude-whatsapp-admin`) so it stays reachable precisely when the bridge is the thing that is broken.

- **Status** — bridge, gowa and Claude account on one screen, with an **All good / Degraded / Down** indicator and the reasons, plus bridge and gowa logs.
- **Who can instruct the bot** — personal or team mode, the one number, the group and its approved members ([above](#who-can-instruct-the-bot)).
- **WhatsApp recovery** — Reconnect (try this first when status says disconnected), pairing by QR, pairing by code, and **Unlink** to move to a different bot number.
- **Sign in / switch Claude account** — sign in or change accounts without opening a terminal.
- **Admin login** — username and password (only a hash is stored), log out, and change the password from the page; see [below](#admin-login).

`Down` + "WhatsApp is logged out" means the WhatsApp session was deleted (for example the device was unlinked from the phone, or the main phone was offline for too long). The bot answers nothing until it is linked again — and no other alarm goes off, so check this page from time to time.

### Admin login

The page is behind its own login: **one account**, username and password chosen at install time. The password is stored only as a salted PBKDF2 hash in `~/.claude-whatsapp/admin.json` (mode `0600`), never in `.env`, and never sent anywhere. A session cookie (`HttpOnly`, `SameSite=Strict`) keeps you signed in for up to 12 hours (30 minutes idle); the admin restarting signs everybody out.

- **Password rules:** at least 10 characters, with at least 5 different ones. No digits or symbols are demanded — a short phrase of unrelated words (with spaces) is fine and easier to remember than a jumble. If a password is refused, the message lists every rule it broke, with the numbers.
- **Change the password** in the *Admin login* card (asks for the current one; every other browser is signed out). **Log out** is at the top right.
- **Guessing is throttled:** after 5 wrong passwords a client is locked out for 5 minutes, doubling each time it happens again (up to an hour).
- **Forgot the password?** On the machine itself run `~/claude-whatsapp/bin/admin passwd` (or `bin/admin passwd` in a source checkout). It can only be run there, by a user who can already read the file, so it needs no login. It also lets you rename the account with `--user`.
- **No account yet** (for example after a manual install or an upgrade from v0.1.x): the login page offers *Create the admin login* — **only to a browser on the machine itself**. From anywhere else (LAN, ZeroTier, SSH tunnel excepted) the page just tells you to do it locally or run `bin/admin passwd`, so nobody else can claim it first.
- This is still plain HTTP on a LAN: the password can be read by others on the same network, and the cookie cannot be marked `Secure`. Prefer ZeroTier (encrypted) or an SSH tunnel, or put TLS in front; never expose the page to the internet.

### Access from another computer

By default the page **listens on `127.0.0.1` only**. From a laptop or phone, use an SSH tunnel:

```bash
ssh -L 8098:127.0.0.1:8098 <user>@<bot-machine-ip>     # then open http://localhost:8098
```

Or serve it directly on your LAN / [ZeroTier](https://www.zerotier.com/) by setting this in `.env` (then `systemctl --user restart claude-whatsapp-admin`):

```bash
ADMIN_ADDR=127.0.0.1:8098,192.168.1.20:8098,10.147.20.15:8098   # this machine's specific IPs; 0.0.0.0 is refused
ADMIN_ALLOWED_NETS=192.168.1.0/24,10.147.0.0/16                  # only clients from these networks are served
```

Clients outside `ADMIN_ALLOWED_NETS` are rejected right away (403), and an IP that doesn't exist yet at boot (e.g. a ZeroTier interface) is retried every 5 seconds. Everyone allowed by the network rules still has to sign in ([above](#admin-login)); until an account exists, other machines are refused. Never expose the page to the public internet.

## Configuration

Everything lives in `.env` in the install directory (`chmod 600`; fully commented template: [`.env.example`](.env.example), reference table: [`docs/ARCHITECTURE.md` §13](docs/ARCHITECTURE.md#13-configuration-env-vars)). The installer fills in what is required; the rest has sensible defaults.

| Variable | Purpose |
|---|---|
| `ALLOWED_SENDERS` | **Required.** Your phone number — the starting point of personal mode (exactly one number; extra entries are ignored). Once you save a policy in the Admin UI it lives in `access.json` and this value is no longer used. |
| `ACCESS_FILE` | Where the access policy is stored (default `~/.claude-whatsapp/access.json`). A broken file makes the bridge deny everybody. |
| `LOG_GROUP_MESSAGES` | `1` logs group message text, sender and the decision — for diagnosing @mention detection. Off by default. |
| `ALLOWED_GROUPS` | **Deprecated** — admits nobody any more. Use team mode in the Admin UI. |
| `WEBHOOK_SECRET` | HMAC key between gowa and the bridge — generated randomly by the installer. |
| `GOWA_BASIC_AUTH_USER/PASSWORD` | Credentials for gowa's REST API — the password is generated randomly by the installer. |
| `WORK_DIR` | Working directory of `claude -p` (default `$HOME`; this is where your `CLAUDE.md` is picked up). |
| `ADMIN_ADDR`, `ADMIN_ALLOWED_NETS` | Where the Admin UI listens and which client networks may reach it — see [above](#access-from-another-computer). |
| `ADMIN_AUTH_FILE` | Where the admin login is stored (default `~/.claude-whatsapp/admin.json`). |
| `ADMIN_PASSWORD`, `ADMIN_USER` | **Legacy bootstrap only:** if no login file exists, this password is hashed into one on the first start (user `ADMIN_USER`, default `admin`); afterwards it is ignored — remove it from `.env`. |

To change `.env`: edit it, then `systemctl --user restart claude-whatsapp.service claude-whatsapp-admin.service` (plus `docker compose up -d` in the install directory if you changed anything gowa-related).

## Upgrade & uninstall

**Upgrade** — run the same install command again. Binaries and scripts are replaced; `.env` and `data/` (the WhatsApp session) are left alone. Your access policy (`~/.claude-whatsapp/access.json`) is kept too. Avoid upgrading during an active conversation: the restart kills any running `claude -p`.

**Upgrading from v0.1.x** — see [`CHANGELOG.md`](CHANGELOG.md) for the full list. What you may notice:

- The bot starts in **personal mode** with the first number in `ALLOWED_SENDERS` (extra entries are ignored, with a warning in the log). DMs from that number work exactly as before.
- **`ALLOWED_GROUPS` no longer admits anybody.** Groups are now team mode: one group, an approved-member roster and a required @mention, set up in the Admin UI.
- **The Admin UI now has a real login instead of `ADMIN_PASSWORD` in `.env`** (see [Admin login](#admin-login)). Coming from **v0.2.0**: your `ADMIN_PASSWORD` is adopted automatically on the first start — sign in as `admin` with it, change it in the *Admin login* card, then delete the line from `.env`. Coming from **v0.1.x** (no password at all): other machines are refused until you create the login — open the page on the machine itself, or run `bin/admin passwd`. HTTP Basic authentication is no longer accepted.

**Uninstall:**

```bash
systemctl --user disable --now claude-whatsapp.service claude-whatsapp-admin.service
rm ~/.config/systemd/user/claude-whatsapp.service ~/.config/systemd/user/claude-whatsapp-admin.service
systemctl --user daemon-reload
cd ~/claude-whatsapp && docker compose down
sudo rm -rf ~/claude-whatsapp ~/.claude-whatsapp   # sudo: files in data/ are owned by the user inside the container
```

Then remove the bot device from the phone (*Linked devices* → pick the device → *Log out*) so the session is really revoked.

## Manual install (from source)

For development, or if you'd rather not use the installer. Additionally requires [Go](https://go.dev/dl/) 1.26+.

```bash
git clone https://github.com/tarkiman/claude-whatsapp.git && cd claude-whatsapp
cp .env.example .env && chmod 600 .env
```

Edit `.env`: set `WEBHOOK_SECRET` (`openssl rand -hex 32`), change `GOWA_BASIC_AUTH_PASSWORD` (in **both** places it appears), and set `ALLOWED_SENDERS` to your sender number (`6281234567890@s.whatsapp.net`). Then:

```bash
docker compose up -d      # gowa + the attachment-permission sidecar
scripts/deploy.sh         # build bridge + admin, install the systemd --user services (idempotent)
```

Then create the admin login with `bin/admin passwd` (you are asked for a username and a password, without echo), and continue with [steps 3 and 4](#3-link-whatsapp) above. `scripts/deploy.sh` is safe to re-run after any code change (rebuilds, regenerates the units with this machine's paths and `$PATH`, restarts explicitly).

<details>
<summary>Pairing WhatsApp without the Admin UI (curl)</summary>

gowa's newer multi-device API (`/devices/{id}/login*`) isn't stable in the current `:latest` image — use the legacy endpoints with an explicit `device_id`:

```bash
source .env
DEVICE_ID=$(curl -s -u "$GOWA_BASIC_AUTH_USER:$GOWA_BASIC_AUTH_PASSWORD" \
  -X POST "http://localhost:${GOWA_PORT:-3011}/devices" -H "Content-Type: application/json" -d '{}' \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["results"]["id"])')

# replace <number> with the bot's number (country code + number, no +)
curl -s -u "$GOWA_BASIC_AUTH_USER:$GOWA_BASIC_AUTH_PASSWORD" \
  "http://localhost:${GOWA_PORT:-3011}/app/login-with-code?phone=<number>&device_id=$DEVICE_ID"

# wait for "is_logged_in": true (the "state" field on /devices can say "connected" prematurely)
curl -s -u "$GOWA_BASIC_AUTH_USER:$GOWA_BASIC_AUTH_PASSWORD" \
  "http://localhost:${GOWA_PORT:-3011}/app/status?device_id=$DEVICE_ID"
```

Enter the `pair_code` (`XXXX-XXXX`) on the phone right away (*Link a device → Link with phone number instead*) before it expires (~2–3 minutes). The session is stored in `./data/whatsapp`, so restarts don't need re-pairing.

</details>

<details>
<summary>Voice-note transcription (optional)</summary>

```bash
scripts/setup-whisper.sh
```

Builds `whisper.cpp` from source and installs the multilingual `base` model to `data/whisper/ggml-base.bin` (~150 MB, one-time download). Needs `cmake` and `ffmpeg`. Without this step voice notes still reach Claude, only as a "ask the sender to type it out" instruction — the feature just switches itself off, nothing breaks.

</details>

## Troubleshooting

Start with the [Admin UI](#admin-ui): the status, the reasons and the logs usually point straight at the problem.

| Symptom | Likely cause | Check |
|---|---|---|
| I can't sign in to the Admin UI | Wrong username or password; *too many attempts* means a temporary lockout (wait, or reset the password on the machine) | On the machine itself: `bin/admin passwd`. From v0.2.0 the old `ADMIN_PASSWORD` from `.env` is the password until you change it |
| The admin page says login setup is only possible on the machine | No account exists yet and you are not browsing from the machine itself | Open `http://127.0.0.1:8098` there, or run `bin/admin passwd` |
| Bot ignores my @mention in the group | Not in team mode, the sender isn't ticked in the roster, the group isn't the selected one, or the text isn't a real @mention of the bot | Admin UI → *Who can instruct the bot*; set `LOG_GROUP_MESSAGES=1` and read the `group-message:` log line |
| Bot silent although the service is `active` | WhatsApp session deleted/unlinked (Admin UI: `Down` + "logged out"), or gowa lost its connection | Admin UI → **Reconnect**, or pair again. `docker logs claude-whatsapp-gowa` |
| No reply at all | `claude` not found on the systemd service's `$PATH`, or not signed in | `journalctl --user -u claude-whatsapp.service -n 50` — look for `executable file not found`; check the **Claude account** card |
| Generic "there was an error on my side" reply | `claude -p` failed/timed out, or another gowa endpoint failed | Same log — the error message is specific |
| Bridge log says "replied ok" but the message never arrives | "replied ok" only means gowa's API answered 2xx, not that WhatsApp sent it — usually gowa is disconnected | Admin UI: `Connected: no` → **Reconnect** |
| Webhook never reaches the bridge | HMAC signature mismatch (`WEBHOOK_SECRET` differs between `.env` and the gowa container), or the bridge isn't running | `docker logs claude-whatsapp-gowa`, `curl localhost:8099/health` |
| Pairing fails / `is_logged_in: false` forever | The "connected" `state` field is premature — only `is_logged_in: true` can be trusted | `curl .../app/status?device_id=...` |
| `… is not implemented yet` | gowa's newer device-manager endpoints (`/devices/{id}/login*`) aren't mature in `:latest` | Use the Admin UI or the legacy endpoints (`/app/login*?device_id=`) |
| Sent a photo/document, Claude says it can't read the file (permission denied) | gowa stores attachments as `0600` owned by the container's internal user | Make sure the sidecar is running: `docker ps \| grep media-perms-fix`; if missing, `docker compose up -d` |
| Installer: "the installation cannot start yet — N prerequisite(s) missing" | Docker, the Compose plugin, access to the Docker daemon, the `claude` CLI on your `PATH`, or systemd is missing | Each missing piece is listed with the command to fix it and nothing was changed — fix them and run the same command again |
| Installer says `claude` is missing although I installed it | It was installed as another user (e.g. root — it lives in *that* user's home), or `~/.local/bin` is not on your `PATH` | Install it again as the regular user that runs the installer. The installer already looks in `~/.local/bin`; for your own shell add `export PATH="$HOME/.local/bin:$PATH"` to `~/.bashrc` |
| Installer: "could not find a release" | No release for your architecture yet, or GitHub is unreachable | Install manually (above) |

## Features

- **Text** — both directions, with per-chat session continuity.
- **Attachments** — images, video, documents (PDFs etc. — Claude reads them directly with its Read tool) and stickers.
- **Voice notes** — transcribed locally (`whisper.cpp`, multilingual, no cloud API) before being sent to `claude -p`. Optional. On a Raspberry Pi 5 (4 CPU threads) the `base` model runs ~2.3x faster than real time.
- **Durability** — messages are written to an on-disk queue (`~/.claude-whatsapp/pending/`) before being acked to gowa and replayed automatically if the bridge died mid-flight.
- **One `claude -p` per chat at a time** — locked per `chat_id`; further messages for the same chat queue instead of fighting over the same `--resume` session.
- **Personal and team modes** — one number in DMs, or one group with an approved roster and required @mention; managed from the Admin UI and applied without a restart ([above](#who-can-instruct-the-bot)).
- **Admin UI** — status, WhatsApp recovery, Claude sign-in and access control, behind its own login ([above](#admin-ui)).

**Not implemented yet:** tool-call approval via emoji reaction, and cross-chat rate limiting (every different chat is its own `claude -p` process, with no cap on how many run in parallel). Contributions and PRs welcome.

## Repository layout

```
claude-whatsapp/
├── cmd/bridge/main.go               # bridge entrypoint
├── cmd/admin/main.go                # Admin UI entrypoint
├── internal/
│   ├── admin/                       # Admin UI handlers + web/index.html (embedded)
│   ├── adminauth/                   # admin login: password hash, sessions, guess limiter
│   ├── access/                      # who may instruct the bot: personal/team policy, roster, mention detection
│   ├── config/                      # read & validate .env
│   ├── gowa/                        # REST client for gowa
│   ├── webhook/                     # HMAC verification, attachment parsing, orchestration
│   ├── claude/                      # exec claude -p, parse JSON
│   ├── session/                     # chat_id -> session_id
│   ├── pending/                     # durable queue — replayed after a crash
│   └── transcribe/                  # exec whisper-cli for voice notes
├── docker-compose.yml               # gowa + permission-fix sidecar
├── deploy/                          # systemd unit templates (bridge, admin)
├── scripts/
│   ├── quick-install.sh             # one-line installer (curl | bash)
│   ├── install.sh                   # .env + gowa (Docker) + systemd services
│   ├── deploy.sh                    # build (if Go + source present) / use bin/ + install services
│   ├── package-release.sh           # cross-compile + one tarball per architecture
│   └── setup-whisper.sh             # whisper.cpp + model (optional)
├── .github/workflows/release.yml    # tag v* -> publish arm64/armv7/amd64 tarballs
├── CHANGELOG.md                     # what changed per release, incl. upgrade notes
└── docs/                            # ARCHITECTURE.md (+ .id.md), images/
```

**Releasing:** `git tag v0.x.0 && git push origin v0.x.0` — the workflow runs the tests, then publishes the tarballs that `quick-install.sh` downloads. Add the release to [`CHANGELOG.md`](CHANGELOG.md) first.

## License

[MIT](LICENSE). The software is provided as is, without warranty of any kind — see also the [security notes](#read-this-first-security).
