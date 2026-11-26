# Seam Issues Tracker

Team repository for the Seam AMS Issues module v3 expansion.

This repo contains **only the files we modify** — not the full Seam source
code.

## How it works

```
seam-issues-tracker/          ← this repo
├── merge.sh                  ← script that overlays our changes with the Seam Source Code 
├── src/modified/             ← files we patch (mirror Seam's directory structure)
│   └── internal/ui/issues.go
├── src/new/                  ← files we create from scratch
└── patches/                  ← .patch files for shared files (e.g. i18n)
```

## Setup

### Prerequisites

- Go 1.26.5+
- The Seam Source Code
- xolu running on port 9090

### Step 1: Clone this repo

```bash
git clone https://github.com/Darlinx25/seam-issues-tracker.git
cd seam-issues-tracker
```

### Step 2: Get the Seam Source Code zip

### Step 3: Run the merge

```bash
./merge.sh seam.zip /tmp/seam-dev
```

This creates a fully buildable Seam project at `/tmp/seam-dev/`
with our changes applied on top.

### Step 4: Build and run

```bash
cd /tmp/seam-dev
```
### Build seam
```
go build -o /tmp/seam-bin ./cmd/seam
```

### Build xolu with the xolu.zip or clone it from github.com/ha1tch/xolu
```
go build -o /tmp/xolu-bin ./cmd/xolu
```

### Terminal 1: Start xolu
```
XOLU_API_V2_ENABLED=true XOLU_BLOB_ENABLED=true /tmp/xolu-bin --port 9090 --base-dir /tmp/xolu-data
```

### Terminal 2: Copy config and start seam
```
cp config.example.json config.json
/tmp/seam-bin serve
```

### Terminal 3 (Optional): Start the QR image renderer

The per-asset QR card (`/assets/{id}`) shows the rendered code when Seam knows
where `seam-2dcodes` is listening. Without it the codes are still generated and
the public `/s/{code}` landing still works — the card just has no image.

Run these from the `seam-2dcodes` repository root (shipped as
`seam-2dcodes-v0.3.0-poc.zip` next to the Seam source).

**Windows (PowerShell):**

```powershell
go build -o seam-2dcodes.exe ./cmd/seam-2dcodes
.\seam-2dcodes.exe -addr :8090
```

Check it is up:

```powershell
curl.exe -s http://127.0.0.1:8090/healthz
# {"blob_backend":false,"status":"ok","version":"0.3.0-poc"}
```

**Linux / macOS (bash):**

```bash
go build -o seam-2dcodes ./cmd/seam-2dcodes
./seam-2dcodes -addr :8090
```

Use `./`, not `.\`: in zsh the backslash escapes the dot, so `.\seam-2dcodes`
is looked up as `.seam-2dcodes` instead of the binary in the current directory.

Check it is up:

```bash
curl -sS http://127.0.0.1:8090/healthz
# {"blob_backend":false,"status":"ok","version":"0.3.0-poc"}
```

Smoke-test the same request Seam sends (SVG, inline base64):

**Windows (PowerShell):**

```powershell
curl.exe -sS -X POST http://127.0.0.1:8090/v1/codes -H "content-type: application/json" -d '{"symbology":"qr","content":"http://127.0.0.1:8080/s/DEMO000000000001","format":"svg","output":"base64","options":{"level":"H","quietZone":16,"width":600,"height":600}}'
```

**Linux / macOS (bash):**

```bash
curl -sS -X POST http://127.0.0.1:8090/v1/codes \
  -H 'content-type: application/json' \
  -d '{"symbology":"qr","content":"http://127.0.0.1:8080/s/DEMO000000000001","format":"svg","output":"base64","options":{"level":"H","quietZone":16,"width":600,"height":600}}'
```

A successful response contains a `data` field with an
`image/svg+xml;base64,...` value; `blob_backend:false` in `healthz` is expected
because Seam uploads the returned image to xolu itself.

Then these keys must be set under `"features"` in the merged `config.json`
(the merged `config.example.json` already ships these values):

```json
{
  "features": {
    "enable_qr": true,
    "qr_prefix": "SEAM",
    "qr_base_url": "http://localhost:8080",
    "qr_render_url": "http://127.0.0.1:8090"
  }
}
```

Check what Seam actually loaded:

```bash
cd /tmp/seam-dev
python3 -c 'import json; f=json.load(open("config.json"))["features"]; print(f["enable_qr"], f["qr_prefix"], f["qr_base_url"], f["qr_render_url"])'
```

Config is read at startup: restart Seam after editing `config.json`.

- `features.enable_qr` must be `true`, otherwise the QR card is disabled.
- `features.qr_prefix` is the prefix of newly generated codes. Changing it only
  affects codes generated afterwards.
- `features.qr_base_url` is the public prefix encoded inside the QR. Seam joins
  it with `/s/<code>` (a bare host is normalized to that path automatically).
  It must be reachable from the phone scanning the code:
  `http://localhost:8080` only works when the scanner is on the same machine;
  for a phone on the LAN use that machine's IP (e.g.
  `http://192.168.1.5:8080`) and allow the port through the firewall.
- `features.qr_render_url` is the seam-2dcodes base URL. Seam appends
  `/v1/codes` and asks for SVG/base64; it only has to be reachable from the
  Seam process, not from the phone. Leaving it empty disables the image: the
  card falls back to the link-only version with no image and no retry button.

Requirements worth knowing:

- xolu must have blob storage enabled: the startup banner lists `blob storage`
  under `Features`. That is why Terminal 1 sets `XOLU_BLOB_ENABLED=true`.
- The renderer needs no xolu credentials. Seam asks for `output: "base64"` and
  uploads the image to xolu itself, which is why `blob_backend` is `false` in
  the health check above.
- `-addr :8090` only has to be reachable from Seam, not from the phone.

### If the QR image still does not appear

- `curl -sS http://127.0.0.1:8090/healthz` fails → renderer is down or listening
  on another port; start it with `./seam-2dcodes -addr :8090`.
- `bind: address already in use` → an old renderer is still running. Find and
  stop it, then start the new one:

  ```bash
  lsof -nP -iTCP:8090 -sTCP:LISTEN
  kill <PID>
  ```

- Health is OK but the card has no image → confirm `features.qr_render_url`,
  restart Seam, then use **Retry image** or **Regenerate** on the asset's QR
  card. An image already stored in xolu keeps encoding the old URL until it is
  re-rendered.
- The scanned QR opens the wrong host → fix `features.qr_base_url` (use the
  machine's LAN IP for phones) and **Regenerate** the code.
- The xolu banner has no `blob storage` → restart it with
  `XOLU_BLOB_ENABLED=true`.

### Terminal 4 (Optional): State-change notifications with seam-comm

The new issue form has a **Receive state notifications** checkbox. When it is
checked, the reporter's email is frozen on the issue and every successful
lifecycle transition (`start`, `resolve`, `close`, `reject`) queues one email
through [seam-comm](../../seam-comm/seam-comm). An empty `comms_url` disables
the whole feature, so Seam still builds and runs without this service.

Run seam-comm from its own repository root, on port **8091** (8090 belongs to
the QR renderer).

**Windows (PowerShell):**

```powershell
go build -o seam-comm.exe ./cmd/seam-comm
$env:SEAM_COMM_LISTEN_ADDR=":8091"
$env:SEAM_COMM_PROVIDER_EMAIL="smtp"
$env:SEAM_COMM_SMTP_ADDR="smtp.example.com:587"
$env:SEAM_COMM_SMTP_FROM="alerts@example.com"
.\seam-comm.exe serve
```

Check it is up:

```powershell
curl.exe -s http://127.0.0.1:8091/healthz
```

**Linux / macOS (bash):**

```bash
go build -o seam-comm ./cmd/seam-comm
export SEAM_COMM_LISTEN_ADDR=":8091"
export SEAM_COMM_PROVIDER_EMAIL="smtp"
export SEAM_COMM_SMTP_ADDR="smtp.example.com:587"
export SEAM_COMM_SMTP_FROM="alerts@example.com"
./seam-comm serve
```

The same `./` vs `.\` caveat as in Terminal 3 applies in zsh.

Check it is up:

```bash
curl -sS http://127.0.0.1:8091/healthz
```

Then set both keys in `config.json` (the merged `config.example.json` already
ships these local defaults):

```
"comms_url":      "http://127.0.0.1:8091",
"comms_api_key":  "dev-key",
```

Scope of this slice: **only email is delivered for real**. SMS and WhatsApp
are simulated by seam-comm's fake provider (`GET /v1/dev/outbox`); the service
keeps everything in memory, so restarting it loses the notification history.
The notification is a best-effort side effect: if seam-comm is down, the issue
still changes state and the failure is only written to the Seam log.

### Step 5 (Optional): Seed demo data

```bash
cd /tmp/seam-dev
python3 seeds/seed-01-constructec.py --url http://localhost:9090
```

Xolu: http://localhost:9090

Seam: http://localhost:8080

### Step 6 (Optional): Build a demo package

```
cd /tmp/seam-dev
```
```
make demo
```
Generates a self-contained ZIP with binaries, pre-loaded database, and
a START.sh launcher. Target machine needs zero dependencies.





