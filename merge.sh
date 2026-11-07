#!/bin/bash
# merge.sh — applies team changes on top of a fresh Seam ZIP.
#
# Usage:
#   ./merge.sh /path/to/seam-vX.Y.Z.zip
#   ./merge.sh /path/to/seam-vX.Y.Z.zip /custom/output/dir
#
# What it does:
#   1. Extracts the ZIP into a temp directory (or the specified output dir)
#   2. Overlays files from src/modified/ (your patched versions)
#   3. Copies files from src/new/ (files that don't exist in Seam)
#   4. Applies .patch files from patches/ (for shared files like i18n)
#   5. Fixes go.mod (removes broken replace directives, runs go mod tidy)
#   6. Registers routes, wires the QR card and merges locales
#   7. Regenerates Tailwind CSS so new utility classes exist
#
# After merge, build with:
#   cd <output-dir> && go build -o /tmp/seam-bin ./cmd/seam

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ZIP="${1:-}"
OUTPUT_DIR="${2:-}"

if [ -z "$ZIP" ]; then
    echo "Usage: $0 /path/to/seam-vX.Y.Z.zip [output-dir]"
    echo ""
    echo "If output-dir is omitted, creates one at /tmp/seam-merge-<timestamp>"
    exit 1
fi

if [ ! -f "$ZIP" ]; then
    echo "Error: ZIP file not found: $ZIP"
    exit 1
fi

# Determine output directory
if [ -z "$OUTPUT_DIR" ]; then
    TIMESTAMP=$(date +%Y%m%d-%H%M%S)
    OUTPUT_DIR="/tmp/seam-merge-${TIMESTAMP}"
fi

echo "=== Seam Issues Tracker — Merge ==="
echo "ZIP:      $ZIP"
echo "Output:   $OUTPUT_DIR"
echo ""

# Step 1: Extract ZIP
echo "[1/7] Extracting ZIP..."
mkdir -p "$OUTPUT_DIR"
unzip -o "$ZIP" -d "$OUTPUT_DIR" > /dev/null
echo "  Extracted to $OUTPUT_DIR"

# Step 2: Overlay modified files
echo "[2/7] Applying modified files..."
MODIFIED_DIR="$SCRIPT_DIR/src/modified"
if [ -d "$MODIFIED_DIR" ]; then
    FIND_CMD=(find "$MODIFIED_DIR" -type f)
    COUNT=0
    while IFS= read -r file; do
        # Compute relative path from src/modified/
        rel="${file#"$MODIFIED_DIR"/}"
        target="$OUTPUT_DIR/$rel"
        mkdir -p "$(dirname "$target")"
        cp "$file" "$target"
        echo "  patched: $rel"
        COUNT=$((COUNT + 1))
    done < <("${FIND_CMD[@]}")
    if [ "$COUNT" -eq 0 ]; then
        echo "  (no modified files)"
    fi
else
    echo "  (src/modified/ not found, skipping)"
fi

# Step 3: Copy new files
echo "[3/7] Adding new files..."
NEW_DIR="$SCRIPT_DIR/src/new"
if [ -d "$NEW_DIR" ]; then
    FIND_CMD=(find "$NEW_DIR" -type f)
    COUNT=0
    while IFS= read -r file; do
        rel="${file#"$NEW_DIR"/}"
        target="$OUTPUT_DIR/$rel"
        mkdir -p "$(dirname "$target")"
        cp "$file" "$target"
        echo "  added: $rel"
        COUNT=$((COUNT + 1))
    done < <("${FIND_CMD[@]}")
    if [ "$COUNT" -eq 0 ]; then
        echo "  (no new files)"
    fi
else
    echo "  (src/new/ not found, skipping)"
fi

# Step 4: Apply patches
echo "[4/7] Applying patches..."
PATCHES_DIR="$SCRIPT_DIR/patches"
if [ -d "$PATCHES_DIR" ] && ls "$PATCHES_DIR"/*.patch > /dev/null 2>&1; then
    for patch in "$PATCHES_DIR"/*.patch; do
        if patch -d "$OUTPUT_DIR" -p1 --forward --silent < "$patch" 2>/dev/null; then
            echo "  applied: $(basename "$patch")"
        else
            echo "  ALREADY APPLIED or CONFLICT: $(basename "$patch")"
        fi
    done
else
    echo "  (no patches found)"
fi

# Step 5: Fix go.mod (remove broken replace directives, update deps)
echo "[5/7] Fixing go.mod..."
cd "$OUTPUT_DIR"
if grep -q "^replace" go.mod 2>/dev/null; then
    echo "  Found replace directive in go.mod — fixing..."
    # Remove all replace lines
    sed -i '/^replace /d' go.mod
    sed -i -e :a -e '/^\n*$/{$d;N;ba' -e '}' go.mod
    rm -f go.sum
    # The xolu version in the ZIP (v0.30.6) was never published — it only
    # worked via the local replace. Update to the latest published version.
    CURRENT_XOLU=$(grep 'ha1tch/xolu' go.mod | awk '{print $2}' | tr -d '"')
    echo "  xolu version in ZIP: $CURRENT_XOLU (not published, replace was required)"
    echo "  Updating xolu to latest published version..."
    # Replace the old version string so go get doesn't try to resolve it
    sed -i "s|github.com/ha1tch/xolu v.*|github.com/ha1tch/xolu v0.30.23|g" go.mod
    go get github.com/ha1tch/xolu@latest 2>/dev/null || true
    go get golang.org/x/crypto@latest 2>/dev/null || true
    go mod tidy
    NEW_XOLU=$(grep 'ha1tch/xolu' go.mod | awk '{print $2}' | tr -d '"')
    echo "  go.mod fixed: xolu updated to $NEW_XOLU, replace directives removed"
else
    echo "  go.mod looks clean, skipping fix"
fi

# Step 6: Register the routes, wire the per-asset QR card and its image
# settings, merge the issues translation keys
echo "[6/7] Registering routes, wiring the QR card and merging locales..."
if [ -f "$OUTPUT_DIR/internal/ui/router.go" ] && [ -f "$SCRIPT_DIR/fix_router.py" ]; then
    python3 "$SCRIPT_DIR/fix_router.py" "$OUTPUT_DIR/internal/ui/router.go"
fi
if [ -f "$SCRIPT_DIR/fix_issue_qr.py" ]; then
    python3 "$SCRIPT_DIR/fix_issue_qr.py" "$OUTPUT_DIR/internal/ui/asset_detail.go" "$OUTPUT_DIR/internal/ui/routes.go"
fi
if [ -f "$SCRIPT_DIR/fix_qr_config.py" ]; then
    python3 "$SCRIPT_DIR/fix_qr_config.py" \
        "$OUTPUT_DIR/internal/config/config.go" \
        "$OUTPUT_DIR/internal/ui/layout.go" \
        "$OUTPUT_DIR/internal/server/server.go" \
        "$OUTPUT_DIR/config.example.json"
fi
if [ -f "$SCRIPT_DIR/merge_locales.py" ]; then
    for L in en es pt ja; do
        LF="$OUTPUT_DIR/internal/i18n/locales/$L.json"
        if [ -f "$LF" ]; then
            python3 "$SCRIPT_DIR/merge_locales.py" "$LF" "$L"
        fi
    done
fi

echo "[7/7] Regenerating Tailwind CSS..."
if command -v npm > /dev/null 2>&1; then
    if [ -f package.json ]; then
        if [ ! -x node_modules/.bin/tailwindcss ]; then
            echo "  Tailwind CLI missing; installing dependencies..."
            npm ci --no-audit --no-fund > /dev/null 2>&1 || npm install --no-audit --no-fund > /dev/null 2>&1 || true
        fi
        if [ -x node_modules/.bin/tailwindcss ]; then
            if npm run css; then
                echo "  CSS regenerated"
            else
                echo "  WARNING: 'npm run css' failed. Run it manually before serving."
            fi
        else
            echo "  WARNING: Tailwind CLI unavailable. Run 'npm install && npm run css' before serving."
        fi
    else
        echo "  (no package.json, skipping)"
    fi
else
    echo "  WARNING: npm not available. Run 'npm install && npm run css' before serving."
fi

echo ""
echo "=== Done ==="
echo ""
echo "  cd $OUTPUT_DIR"
echo "  go build -o /tmp/seam-bin ./cmd/seam"
echo "  cd "/path/to/xolu""
echo "  go build -o /tmp/xolu-bin ./cmd/xolu"
echo ""
echo "  # Terminal 1: Start xolu"
echo "  XOLU_API_V2_ENABLED=true /tmp/xolu-bin --port 9090 --base-dir /tmp/xolu-data"
echo "  (v2 API required — FSM de Issues usa /api/v2; sin esto, crear un issue da 500)"
echo ""
echo "  # Terminal 2: Copy config and start seam"
echo "  cp config.example.json config.json"
echo "  /tmp/seam-bin serve"
echo ""
echo "  Xolu: http://localhost:9090"
echo "  Seam: http://localhost:8080"
