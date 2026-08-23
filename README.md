# Seam Issues Tracker

Team repository for the Seam AMS Issues module v3 expansion.

This repo contains **only the files we modify** — not the full Seam source
code (which is proprietary and distributed by Aberton as weekly ZIPs).

## How it works

```
seam-issues-tracker/          ← this repo (public on GitHub)
├── merge.sh                  ← script that overlays our changes onto a Seam ZIP
├── src/modified/             ← files we patch (mirror Seam's directory structure)
│   └── internal/ui/issues.go
├── src/new/                  ← files we create from scratch
└── patches/                  ← .patch files for shared files (e.g. i18n)
```

## Setup (for team members)

### Prerequisites

- Go 1.26.5+
- The Seam ZIP from Aberton (e.g. `seam-v0.4.0-2026-08-18.zip`)
- xolu running on port 9090 (see below)

### Step 1: Clone this repo

```bash
git clone https://github.com/<your-org>/seam-issues-tracker.git
cd seam-issues-tracker
```

### Step 2: Get the Seam ZIP

Ask Facundo for the latest ZIP, or download it from wherever Aberton
distributes it. Place it anywhere on your machine.

### Step 3: Run the merge

```bash
./merge.sh /path/to/seam-v0.4.0-2026-08-18.zip
```

This creates a fully buildable Seam project at `/tmp/seam-merge-<timestamp>/`
with our changes applied on top.

### Step 4: Build and run

```bash
cd /tmp/seam-merge-*/

# Build seam
go build -o /tmp/seam-bin ./cmd/seam

# You also need xolu running — build it from its own source:
# (ask Facundo for the xolu ZIP or clone from github.com/ha1tch/xolu)
go build -o /tmp/xolu-bin ./cmd/xolu

# Terminal 1: Start xolu
/tmp/xolu-bin serve --dev

# Terminal 2: Copy config and start seam
cp config.example.json config.json
# Edit config.json if needed (xolu should be at http://localhost:9090)
/tmp/seam-bin serve
```

### Step 5: Login

- URL: http://localhost:8080
- Email: `horacio.lopez@gmail.com`
- Password: `holaquetal`

## Development workflow

1. **Edit files** in `src/modified/` (this is your working copy)
2. **Commit and push** from this repo — only your changes go to GitHub
3. **Merge and build** when you need to test:
   ```bash
   ./merge.sh /path/to/seam-v0.4.0-2026-08-18.zip /tmp/seam-test
   cd /tmp/seam-test && go build -o /tmp/seam-bin ./cmd/seam
   ```
4. **Run tests** after merge:
   ```bash
   cd /tmp/seam-test && go test ./internal/ui/ -run TestIssue -v
   ```

## Directory structure explained

| Directory | Purpose | What goes here |
|-----------|---------|----------------|
| `src/modified/` | Files we patch over Seam's originals | `issues.go`, `router.go`, `create.json`, `en.json`, etc. |
| `src/new/` | Files that don't exist in Seam | New modules, new widgets, etc. |
| `patches/` | .patch files for shared files | When we only add keys to i18n, not replace the whole file |

### Why not modify Seam directly?

- Seam is distributed as ZIPs by Aberton — we don't control the source
- Each week a new ZIP arrives — our changes need to be re-applied cleanly
- If we modified Seam directly, we'd have to manually diff/merge every week
- This repo isolates OUR changes so they're easy to track, review, and apply

### What about go.mod?

Seam's go.mod has a broken `replace` directive pointing to a local path.
The merge script fixes this automatically (removes the replace, runs
`go mod tidy`). We don't store go.mod/go.sum in this repo because they
regenerate correctly after the fix.

## Important rules

- **Code in English** — all Go code, variable names, function names, comments
- **User-visible text via i18n** — never string literals, always `t("namespace.key")`
- **Internal identifiers always English** — states, constants, validation values
- **4 locales**: en, es, pt, ja — every user-facing key must exist in all 4
- **Follow STACK-01/02/03** — the .docx guides from the university

## Links

- [xolu (database server)](https://github.com/ha1tch/xolu)
- [minty (HTML components)](https://github.com/ha1tch/minty)
- [Seam AMS docs](./docs/) (in the ZIP from Aberton)
