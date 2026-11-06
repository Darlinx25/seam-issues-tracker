#!/usr/bin/env python3
"""Insert the issue action routes into MountIssuesRoutes.

Idempotent: each group of routes is only inserted when it is missing, so the
file is left untouched once everything is registered. Only the route lines are
added, right after the reject routes, so upstream changes elsewhere in the
block are preserved.
"""
import sys

ASSIGN_ROUTES = [
    '\tmux.HandleFunc("GET /issues/{id}/assign", r.requireAuth(r.handler.IssueAssignFormPage))\n',
    '\tmux.HandleFunc("POST /issues/{id}/assign", r.requireAuth(r.handler.IssueAssign))\n',
]

# Grouping into a case (iteration 2). The literal "/issues/group" pattern wins
# over "/issues/{id}" by ServeMux precedence, the same way "/issues/new" does.
GROUP_ROUTES = [
    '\tmux.HandleFunc("GET /issues/group", r.requireAuth(r.handler.IssueGroupFormPage))\n',
    '\tmux.HandleFunc("POST /issues/group", r.requireAuth(r.handler.IssueGroup))\n',
    '\tmux.HandleFunc("POST /issues/{id}/ungroup", r.requireAuth(r.handler.IssueUngroup))\n',
]

REJECT_ROUTE = '\tmux.HandleFunc("POST /issues/{id}/reject", r.requireAuth(r.handler.IssueReject))\n'

# Per-asset QR codes (issue_qr_codes). Registered next to the asset detail
# route, inside the same EnableOperational block: the card is part of the
# asset screen. Mutations use requirePerm(PermAssetsAssetEdit) on top of the
# handler's own check; reading the card only needs a session.
ASSET_DETAIL_ROUTE = '\tmux.HandleFunc("GET /assets/{id}", r.requireAuth(r.handler.AssetDetail))'

QR_ROUTES = [
    '\tmux.HandleFunc("GET /assets/{id}/qr/card", r.requireAuth(r.handler.AssetQRCodeCard))',
    '\tmux.HandleFunc("POST /assets/{id}/qr/generate", r.requirePerm(auth.PermAssetsAssetEdit, r.handler.AssetQRCodeGenerate))',
    '\tmux.HandleFunc("POST /assets/{id}/qr/regenerate", r.requirePerm(auth.PermAssetsAssetEdit, r.handler.AssetQRCodeRegenerate))',
    '\tmux.HandleFunc("POST /assets/{id}/qr/revoke", r.requirePerm(auth.PermAssetsAssetEdit, r.handler.AssetQRCodeRevoke))',
]


def main(path):
    patch_issue_routes(path)
    patch_asset_qr_routes(path)


def patch_issue_routes(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()

    start = s.find('func (r *Router) MountIssuesRoutes')
    if start == -1:
        print("  router: MountIssuesRoutes not found, skipping")
        return

    end = s.find('\nfunc ', start + 10)
    if end == -1:
        end = len(s)

    block = s[start:end]
    if REJECT_ROUTE not in block:
        print("  router: reject route anchor not found, skipping")
        return

    changed = False
    if '"GET /issues/{id}/assign"' not in block or '"POST /issues/{id}/assign"' not in block:
        block = block.replace(REJECT_ROUTE, REJECT_ROUTE + ''.join(ASSIGN_ROUTES), 1)
        changed = True
        print("  router: assign routes inserted")
    if '"GET /issues/group"' not in block:
        block = block.replace(REJECT_ROUTE, REJECT_ROUTE + ''.join(GROUP_ROUTES), 1)
        changed = True
        print("  router: grouping routes inserted")

    if not changed:
        print("  router: issue routes already present")
        return

    s = s[:start] + block + s[end:]
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s)


def patch_asset_qr_routes(path):
    """Register the per-asset QR routes in MountAssetRoutes.

    Split out because the anchor lives in a different mount function than the
    issue routes. Only the route lines are added, right after the asset detail
    route, so upstream changes elsewhere in the block are preserved.
    """
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()

    start = s.find('func (r *Router) MountAssetRoutes')
    if start == -1:
        print("  router: MountAssetRoutes not found", file=sys.stderr)
        sys.exit(1)

    end = s.find('\nfunc ', start + 10)
    if end == -1:
        end = len(s)

    block = s[start:end]
    if '/assets/{id}/qr/card' in block:
        print("  router: asset QR routes already present")
        return

    pos = block.find(ASSET_DETAIL_ROUTE)
    if pos == -1:
        print("  router: asset detail route anchor not found", file=sys.stderr)
        sys.exit(1)

    nl = '\r\n' if '\r\n' in block else '\n'
    after = pos + len(ASSET_DETAIL_ROUTE) + len(nl)
    # Match the anchor's indentation: the asset routes live inside a block, so
    # the anchor is indented deeper than the function body.
    line_start = block.rfind('\n', 0, pos) + 1
    # The anchor pattern carries its own leading tab, so take one char past pos
    # to reproduce the full indentation of the surrounding block.
    indent = block[line_start:pos + 1]
    block = block[:after] + ''.join(indent + r.lstrip('\t') + nl for r in QR_ROUTES) + block[after:]

    s = s[:start] + block + s[end:]
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s)
    print("  router: asset QR routes inserted")


if __name__ == '__main__':
    main(sys.argv[1])