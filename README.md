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
/tmp/xolu-bin --port 9090 --base-dir /tmp/xolu-data
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
`seam-2dcodes-v0.3.0-poc.zip` next to the Seam source):

```
go build -o seam-2dcodes.exe ./cmd/seam-2dcodes
.\seam-2dcodes.exe -addr :8090
```

Check it is up:

```
curl.exe -s http://127.0.0.1:8090/healthz
# {"blob_backend":false,"status":"ok","version":"0.3.0-poc"}
```

Then both QR keys must be set in `config.json` (the merged
`config.example.json` already ships these values):

```
"qr_base_url":   "http://localhost:8080",
"qr_render_url": "http://127.0.0.1:8090",
```

- `qr_base_url` is the host printed on the label, so it must be reachable from
  the phone scanning the code. On a desktop that is `http://localhost:8080`; for
  a phone on the LAN use the machine's LAN IP instead (e.g.
  `http://192.168.1.5:8080`), and allow the port through the firewall.
- `qr_render_url` is this service. Empty disables the image entirely: the card
  falls back to the link-only version with no image and no retry button.

Requirements worth knowing:

- xolu must have blob storage enabled: the startup banner lists `blob storage`
  under `Features`. If it is missing, start xolu with `XOLU_BLOB_ENABLED=true`.
- The renderer needs no xolu credentials. Seam asks for `output: "base64"` and
  uploads the image to xolu itself, which is why `blob_backend` is `false` in
  the health check above.
- `-addr :8090` only has to be reachable from Seam, not from the phone.

Changing either value requires restarting Seam (config is read at startup). The
image already stored in xolu keeps encoding the old URL until it is re-rendered:
use **Retry image** (or **Regenerate**) on the asset's QR card.

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





