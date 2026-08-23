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

### Step 2: Get the Seam Source Code

### Step 3: Run the merge

```bash
./merge.sh seam.zip /tmp/seam-dev
```

This creates a fully buildable Seam project at `/tmp/seam-dev/`
with our changes applied on top.

### Step 4: Build and run

```bash
cd /tmp/seam-dev

# Build seam
go build -o /tmp/seam-bin ./cmd/seam

# Build xolu (clone from github.com/ha1tch/xolu)
go build -o /tmp/xolu-bin ./cmd/xolu

# Terminal 1: Start xolu
/tmp/xolu-bin serve --dev

# Terminal 2: Copy config and start seam
cp config.example.json config.json
# Edit config.json if needed (xolu should be at http://localhost:9090)
/tmp/seam-bin serve
```








