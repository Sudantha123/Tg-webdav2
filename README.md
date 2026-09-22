# 🦋 TgWebDAV

**Telegram channel එකක් unlimited cloud drive එකක් විදිහට පාවිච්චි කරන, Go වලින් ලියපු lightweight WebDAV + Web UI server එකක්.**

> Bot inbox එකට දාන file → channel එකට auto forward වෙනවා → `/dav` WebDAV mount එකේ සහ `/web` UI එකේ ඒ ක්ෂණිකම පේනවා. Videos seek කරන්නත් පුළුවන් (HTTP Range streaming), මුළු file එකම download නොකර.

| | |
|---|---|
| **WebDAV** | `http://vps_ip:PORT/dav/` |
| **Web UI** | `http://vps_ip:PORT/web/` |
| **Login** | `.env` එකේ දාන `WEB_USER` / `WEB_PASS` (Basic Auth) |

---

## ✨ Features

- 📥 **Bot ingestion** — bot ගේ inbox එකට දාන හැම file එකක්ම channel එකට auto forward වෙලා index වෙනවා
- 📁 **`general` default folder** — caption නැත්නම් ඒකට යනවා; web UI එකෙන් (⚙ Settings) ඕනෑම වෙලාවක වෙනස් කරන්න පුළුවන්
- 🏷️ **Caption → filename** — caption එකේ තියෙන නමින්ම file එක පේනවා
  - `movies/My Film.mp4` වගේ caption දැම්මොත් **folder + name** දෙකම set වෙනවා
  - caption නැත්නම් telegram file name එක, ඒකත් නැත්නම් MIME-aware random name (`video_20260922_141530_a3f1.mp4`)
- 🎬 **Video seek / streaming** — HTTP Range requests, 1 MiB chunk cache + prefetch → විශාල videos වල seek ඉබේම වැඩ
- ⚡ **Fast & lightweight** — single static binary (~12 MB), SQLite (pure-Go) metadata index, PROPFIND/listings සියල්ලම DB එකෙන් (Telegram latency නැහැ), RAM cache configurable
- 📤 **Full WebDAV** — PROPFIND, GET, PUT, MKCOL, DELETE, MOVE, COPY (COPY/MOVE channel level එකේම වැඩ — bytes server එකෙන් pass වෙන්නේ නැහැ), LOCK support
- 🌐 **Web UI** — login, folders, upload (drag & drop), rename/move/delete/copy, video/audio/image preview, settings, stats
- 🔐 **Basic Auth** — `WEB_USER`/`WEB_PASS` + optional extra users (`WEB_USERS`)
- 🐳 **Docker + docker-compose** support
- 🤖 **GitHub Actions** — push කරන හැම වෙලාවෙම build+test; tag දැම්මම release binaries (linux amd64/arm64, windows, macOS)

---

## 🧱 Architecture

```
                    ┌──────────────────────────── VPS ────────────────────────────┐
 Telegram users ──▶ │ Bot API (long-poll)  ──▶ forward ──▶ Telegram channel       │
                    │                              │                               │
 Browser / WebDAV ─▶ :8080  /web (UI)  /dav (WebDAV) │                               │
                    │            │                   ▼                               │
                    │       SQLite index ◀──── gotd MTProto (bot login)            │
                    │            │          Range chunks + 1MiB LRU cache          │
                    └────────────┼─────────────────────────────────────────────────┘
                                 └── clients: RaiDrive / rclone / RCX / browser …
```

- **Metadata** (names, folders, sizes) ගබඩා වෙන්නේ local **SQLite** එකේ → folder listing instant.
- **File content** කියවන්නේ කෙලින්ම **Telegram channel** එකෙන් (MTProto) → VPS disk එකේ file නැහැ.
- **PUT/uploads** stream වෙනවා temp file එකකට, ඊට පස්සේ Telegram එකට upload වෙනවා.

---

## 🚀 VPS Setup (Quick Start)

### 1️⃣ Telegram side setup

1. **Bot token** — Telegram එකේ [@BotFather](https://t.me/BotFather) ට `/newbot` → token එක copy කරගන්න (`123456:AA...`).
2. **API_ID / API_HASH** — [my.telegram.org](https://my.telegram.org) → *API development tools* → app එකක් හදලා `api_id` + `api_hash` ගන්න. (MTProto streaming වලට අනිවාර්යයි — මේකෙන් තමයි 20MB ට වඩා files stream කරන්නේ.)
3. **Channel එක** — අලුත් private channel එකක් හදන්න (e.g. "MyDrive").
4. **Bot ව channel එකට admin කරන්න** — Channel → Administrators → Add Admin → ඔයාගේ bot තෝරලා *Post Messages* + *Delete Messages* rights දෙන්න.
5. **Channel ID** — channel එකේ ඕනෑම post එකක් bot ට forward කරලා [@userinfobot](https://t.me/userinfobot) වගේ bot එකකින් හෝ [@getidsbot](https://t.me/getidsbot) එකෙන් channel id එක ගන්න. Format එක: `-1001234567890`.

### 2️⃣ Binary එක download කරලා run කරන්න (amd64 VPS)

```bash
# latest release binary එක ගන්න (x86_64/amd64)
sudo mkdir -p /opt/tgwebdav && cd /opt/tgwebdav
sudo curl -fL -o tgwebdav \
  "https://github.com/Sudantha123/Tg-webdav2/releases/latest/download/tgwebdav-linux-amd64"
sudo chmod +x tgwebdav
```

> නැත්නම් installer script එක: `curl -fsSL https://raw.githubusercontent.com/Sudantha123/Tg-webdav2/main/install.sh | sudo bash -s --`

### 3️⃣ `.env` file එක හදන්න

```bash
sudo nano /opt/tgwebdav/.env
```

```ini
BOT_TOKEN=123456789:AAxxxxxxxxxxxxxxxxxxxx
API_ID=1234567
API_HASH=0123456789abcdef0123456789abcdef
CHANNEL_ID=-1001234567890

PORT=8080
WEB_USER=admin
WEB_PASS=Str0ngPassw0rd!
DEFAULT_FOLDER=general

DATA_DIR=/opt/tgwebdav/data
CACHE_MB=64
PREFETCH=8
DELETE_FROM_TELEGRAM=true
LOG_LEVEL=info
```

(සම්පූර්ණ list එක [.env.example](.env.example) එකේ තියෙනවා.)

### 4️⃣ Test run එකක්

```bash
cd /opt/tgwebdav && ./tgwebdav
```

```
INFO telegram connected
INFO bot ready username=@mydrive_bot
INFO tgwebdav started port=8080 webdav=http://<vps_ip>:8080/dav/ web=http://<vps_ip>:8080/web/
```

Browser එකෙන් `http://VPS_IP:8080/web/` එකට ගිහින් login වෙන්න. Bot ට file එකක් එවලා බලන්න! 🎉

### 5️⃣ systemd service එකක් (auto start)

```bash
sudo tee /etc/systemd/system/tgwebdav.service > /dev/null <<'EOF'
[Unit]
Description=TgWebDAV - Telegram backed WebDAV server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/tgwebdav
EnvironmentFile=/opt/tgwebdav/.env
ExecStart=/opt/tgwebdav/tgwebdav
Restart=always
RestartSec=5
# hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now tgwebdav
sudo systemctl status tgwebdav
```

Logs: `journalctl -u tgwebdav -f`

---

## 🐳 Docker වලින් run කරන්න

```bash
git clone https://github.com/Sudantha123/Tg-webdav2 && cd Tg-webdav2
cp .env.example .env && nano .env      # values ටික දාන්න
docker compose up -d
```

---

## 🔨 Source එකෙන් build කරන්න

Go **1.26+** ඕනේ:

```bash
git clone https://github.com/Sudantha123/Tg-webdav2 && cd Tg-webdav2
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o tgwebdav .
```

Tests: `go test ./...`

---

## ⚙️ Configuration reference (.env)

| Variable | Default | විස්තරය |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `BOT_TOKEN` | — | @BotFather ගෙන් ගන්න bot token එක |
| `API_ID` / `API_HASH` | — | my.telegram.org app credentials (streaming වලට) |
| `CHANNEL_ID` | — | storage channel එකේ id (`-100...`) |
| `DEFAULT_FOLDER` | `general` | inbox uploads වලට default folder එක (web UI එකෙනුත් වෙනස් කරන්න පුළුවන්) |
| `ALLOWED_USER_IDS` | (හැමෝටම) | bot එක පාවිච්චි කරන්න පුළුවන් telegram user ids (comma list) |
| `WEB_USER` / `WEB_PASS` | — | WebDAV + Web UI login |
| `WEB_USERS` | — | අමතර logins: `user1:pass1,user2:pass2` |
| `DATA_DIR` | `./data` | SQLite + session + tmp ගබඩා වෙන තැන |
| `CACHE_MB` | `64` | RAM එකේ තියෙන chunk cache එකේ size එක |
| `PREFETCH` | `8` | streaming වලදී කලින් කියවාගන්න 1 MiB blocks ගාන |
| `DELETE_FROM_TELEGRAM` | `true` | file delete කරද්දී channel post එකත් delete කරන්නද |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

---

## 📖 පාවිච්චි කරන හැටි

### Bot එකට file එවන විදිහ

| ඔයා එවන දේ | WebDAV එකේ පේන්නේ |
|---|---|
| caption: `My Movie.mp4` | `/general/My Movie.mp4` |
| caption: `movies/My Movie.mp4` | `/movies/My Movie.mp4` |
| caption නැති file (name තියෙනවා) | `/general/<telegram filename>` |
| caption නැති photo/video | `/general/video_20260922_141530_a3f1.mp4` |

- Default folder එක **Web UI → ⚙ Settings** එකෙන් වෙනස් කරන්න පුළුවන්.
- Bot එකට text message එකක් එව්වොත් help එක එනවා.

### WebDAV client connect කරන හැටි

`http://VPS_IP:8080/dav/` + `WEB_USER`/`WEB_PASS`:

- **Windows** — [RaiDrive](https://www.raidrive.com/) / WinSCP / Mountain Duck → "Drive map"
- **Android** — RCX / Rounded / FolderSync / X-plore (WebDAV)
- **iOS** — Documents by Readdle / WebDAV Navigator
- **Linux** — `rclone` හෝ davfs2:

```bash
rclone config   # type: webdav, url: http://VPS_IP:8080/dav/, vendor: other
rclone ls tgwebdav:
rclone copy movie.mp4 tgwebdav:movies/
```

### Web UI (`/web/`)

- 📂 folder browse + search
- ⬆ drag & drop upload (progress එකත් එක්ක)
- ▶ video/audio/image preview (seek support)
- ✏️ rename · 📤 move · 🔗 direct link copy · 🗑 delete
- ⚙ default folder settings · 💾 stats bar (files / size / cache)

---

## 🤖 GitHub Actions builds

- **CI** (`.github/workflows/ci.yml`) — හැම push එකකටම `go mod tidy` → `go vet` → `go test` → **linux/amd64** build (+ arm64/windows/darwin). Build fail වුණොත් Actions tab එකේ log එක බලලා fix කරන්න. `go.sum` නැතුව first push එකදී bot එක auto-commit කරනවා.
- **Release** (`.github/workflows/release.yml`) — `v1.2.3` වගේ tag එකක් push කරාම (නැත්නම් Actions → Release → Run workflow) release binaries + checksums GitHub Release එකක් විදිහට attach වෙනවා.

```bash
git tag v1.0.0 && git push origin v1.0.0   # release එකක් හදන්න
```

---

## ❓ Troubleshooting

| ප්‍රශ්නය | විසඳුම |
|---|---|
| `channel not found — add the bot to the channel` | Bot ව channel එකට **admin** විදිහට add කරලා restart කරන්න |
| Bot inbox එකේ files index වෙන්නේ නැහැ | Bot token එක හරිද බලන්න; `CHANNEL_ID` එක `-100...` format ද බලන්න; `journalctl -u tgwebdav -f` බලන්න |
| `getUpdates conflict` error | තවත් process එකක් (webhook) එම token එක පාවිච්චි කරනවා — ඒක නවත්තන්න |
| Video seek වැඩ නැහැ | Client එකේ Range support තියෙන්න ඕනේ (rclone/RaiDrive/බ්‍රව්සර් සියල්ලටම තියෙනවා); `CACHE_MB`/`PREFETCH` වැඩි කරන්න |
| 401 Unauthorized | `.env` එකේ `WEB_USER`/`WEB_PASS` හරිද බලන්න |
| Download මදි/slow | VPS↔Telegram latency; `PREFETCH=16`, `CACHE_MB=128` try කරන්න |

---

## 📜 License

MIT — නිදහසේ පාවිච්චි කරන්න. 
