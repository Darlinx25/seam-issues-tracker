#!/usr/bin/env python3
"""Wire the per-asset QR feature into the base screens.

The base files are upstream files, so the overlay cannot express these hooks and
the merge rewrites them in place:

  internal/ui/asset_detail.go  the static QR card is replaced by a loader that
                               pulls the card fragment from the new handler.
  internal/ui/routes.go        QRScan resolves issue_qr_codes first, so the
                               codes generated from the asset detail work, and
                               a revoked code gets its own public message; the
                               public page is then the anonymous report form,
                               with the category frozen into the code.

Idempotent: each hook is skipped once it is already present. If neither the
original text nor the hook is found, the merge fails loudly instead of
silently shipping a build where the feature is unreachable.
"""
import sys

ASSET_DETAIL_OLD = 'Card(t("asset.qr_code_title"), assetQRContent(data.Asset.QRCode, t))(b),'
ASSET_DETAIL_NEW = 'Card(t("asset.qr_code_title"), issueQRCardLoader(data.Asset.ID, t))(b),'

ROUTES_OLD = [
    '\t// Look up QR code -> asset',
    '\tresult, err := h.xoluClient.OQL(ctx, "SELECT TOP 1 * FROM qr_codes WHERE code = \'"+code+"\'")',
    '\tif err != nil || len(result.Data) == 0 {',
    '\t\th.page.RenderPublicError(w, 404, t("qr.not_found_title"), t("qr.not_found_message"))',
    '\t\treturn',
    '\t}',
    '',
    '\tqrData := result.Data[0]',
    '\tassetID := toInt64(qrData["asset_id"])',
    '',
    '\tif assetID == 0 {',
    '\t\t// QR code exists but not assigned to asset',
    '\t\th.page.RenderPublic(w, t("qr.unassigned_title"), QRUnassignedPage(code, t))',
    '\t\treturn',
    '\t}',
]

ROUTES_NEW = [
    '\t// Look up QR code -> asset. Per-asset issue QR codes (issue_qr_codes)',
    '\t// take precedence over the legacy qr_codes table; a revoked code',
    '\t// explains itself instead of looking like a broken link.',
    '\tassetID, found, revoked := h.resolveScanCode(ctx, code)',
    '\tif revoked {',
    '\t\th.page.RenderPublicError(w, http.StatusGone, t("qr.revoked_title"), t("qr.revoked_message"))',
    '\t\treturn',
    '\t}',
    '\tif !found {',
    '\t\th.page.RenderPublicError(w, 404, t("qr.not_found_title"), t("qr.not_found_message"))',
    '\t\treturn',
    '\t}',
    '\tif assetID == 0 {',
    '\t\t// QR code exists but not assigned to asset',
    '\t\th.page.RenderPublic(w, t("qr.unassigned_title"), QRUnassignedPage(code, t))',
    '\t\treturn',
    '\t}',
]

# Two shapes of the render tail exist in the wild and both mean the same thing
# ("hand the scan data to the old landing"), so the hook accepts either:
#   - a pristine release: the landing lives in pages.go and the call is one line;
#   - a tree that already went through the earlier read-only landing patch.
ROUTES_RENDER_CANDIDATES = [
    [
        '\th.page.RenderPublic(w, data.AssetName, QRScanPage(data, t))',
    ],
    [
        '\t// The public landing is read-only until the QR reporting channel is',
        '\t// implemented: no dead action buttons. The full-detail link only makes',
        '\t// sense for a viewer allowed to open the authenticated asset page;',
        '\t// anonymous scanners get a sign-in link back to the asset instead.',
        '\tcanViewDetails := false',
        '\tif h.sessionManager != nil {',
        '\t\tif session, err := h.sessionManager.ValidateSession(r); err == nil {',
        '\t\t\tcanViewDetails = auth.HasPermission(session.Role, auth.PermAssetsAssetView)',
        '\t\t}',
        '\t}',
        '\tassetCategory := assetCategoryFromStoredCriticality(toString(asset.Data["criticality"]))',
        '',
        '\th.page.RenderPublic(w, data.AssetName, issueQRScanPage(data, assetCategory, canViewDetails, t))',
    ],
]

ROUTES_RENDER_NEW = [
    '\t// The landing is the anonymous report form. The category is the one',
    '\t// frozen into the code; only a legacy qr_codes row (which carries none)',
    '\t// falls back to the asset.',
    '\tassetCategory := h.issueQRCodeCategory(ctx, code, asset)',
    '',
    '\th.RenderIssueQRReportLanding(w, r, data, assetCategory)',
]


def _newline(s):
    return '\r\n' if '\r\n' in s else '\n'


def _read(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        return f.read()


def _write(path, s):
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s)


def patch_asset_detail(path):
    s = _read(path)
    if 'issueQRCardLoader' in s:
        print("  asset_detail: QR card already wired")
        return True
    if ASSET_DETAIL_OLD not in s:
        print("  asset_detail: QR card anchor not found", file=sys.stderr)
        return False
    _write(path, s.replace(ASSET_DETAIL_OLD, ASSET_DETAIL_NEW, 1))
    print("  asset_detail: QR card wire to the issue_qr fragment")
    return True


def patch_routes_scan(path):
    s = _read(path)
    if 'resolveScanCode' in s:
        print("  routes: QR scan already resolves issue_qr_codes")
        return True
    nl = _newline(s)
    old = nl.join(ROUTES_OLD) + nl
    if old not in s:
        print("  routes: QRScan anchor not found", file=sys.stderr)
        return False
    _write(path, s.replace(old, nl.join(ROUTES_NEW) + nl, 1))
    print("  routes: QRScan resolves issue_qr_codes and revoked codes")
    return True


def patch_routes_landing(path):
    s = _read(path)
    if 'RenderIssueQRReportLanding' in s:
        print("  routes: QRScan already renders the report form")
        return True
    nl = _newline(s)
    for candidate in ROUTES_RENDER_CANDIDATES:
        old = nl.join(candidate) + nl
        if old in s:
            _write(path, s.replace(old, nl.join(ROUTES_RENDER_NEW) + nl, 1))
            print("  routes: QRScan renders the anonymous report form")
            return True
    print("  routes: QRScan render anchor not found (expected either the "
          "pristine QRScanPage tail or the read-only landing tail)", file=sys.stderr)
    return False


def main(asset_detail_path, routes_path):
    ok = patch_asset_detail(asset_detail_path)
    ok = patch_routes_scan(routes_path) and ok
    ok = patch_routes_landing(routes_path) and ok
    if not ok:
        sys.exit(1)


if __name__ == '__main__':
    main(sys.argv[1], sys.argv[2])