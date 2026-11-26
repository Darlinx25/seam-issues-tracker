#!/usr/bin/env python3
"""Insert the issue action routes into MountIssuesRoutes.

Idempotent and compatible with both the old r.requireAuth(...) wrappers and
the authed(...) helpers used by newer bases.
"""
import re
import sys


def nl_of(text):
    return '\r\n' if '\r\n' in text else '\n'


def auth_call(handler, wrapper):
    if wrapper == 'authed':
        return f'authed(r.handler.{handler})'
    return f'r.requireAuth(r.handler.{handler})'


def perm_call(perm, handler, wrapper):
    if wrapper == 'permed':
        return f'permed(auth.{perm}, r.handler.{handler})'
    return f'r.requirePerm(auth.{perm}, r.handler.{handler})'


def line_for(method, path, handler, wrapper, nl, perm=None):
    call = perm_call(perm, handler, wrapper) if perm else auth_call(handler, wrapper)
    return f'mux.HandleFunc("{method} {path}", {call}){nl}'


def find_line(block, path):
    match = find_line_optional(block, path)
    if match is None:
        print(f'  router: anchor not found for {path}', file=sys.stderr)
        sys.exit(1)
    return match


def find_line_optional(block, path):
    pattern = r'^[ \t]*mux\.HandleFunc\("' + re.escape(path) + r'".*$'
    return re.search(pattern, block, re.MULTILINE)


def wrapper_of(line, path):
    if 'authed(' in line or 'permed(' in line:
        return 'authed', 'permed'
    if 'r.requireAuth(' in line or 'r.requirePerm(' in line:
        return 'requireAuth', 'requirePerm'
    print(f'  router: unknown wrapper for {path}: {line.strip()}', file=sys.stderr)
    sys.exit(1)


def indent_of(match):
    return re.match(r'[ \t]*', match.group(0)).group(0)


def insert_after(block, match, lines):
    indent = indent_of(match)
    pos = match.end()
    if pos < len(block) and block[pos] == '\n':
        pos += 1
    return block[:pos] + ''.join(indent + line for line in lines) + block[pos:]


def replace_line(block, match, lines):
    indent = indent_of(match)
    end = match.end()
    if end < len(block) and block[end] == '\n':
        end += 1
    return block[:match.start()] + ''.join(indent + line for line in lines) + block[end:]


def lifecycle_lines(auth_wrapper, perm_wrapper, nl):
    lines = [
        line_for('POST', '/issues/{id}/start', 'IssueStart', auth_wrapper, nl),
        line_for('POST', '/issues/{id}/resolve', 'IssueResolve', auth_wrapper, nl),
        line_for('POST', '/issues/{id}/close', 'IssueClose', auth_wrapper, nl),
        line_for('GET', '/issues/{id}/reject', 'IssueRejectFormPage', auth_wrapper, nl),
        line_for('POST', '/issues/{id}/reject', 'IssueReject', auth_wrapper, nl),
        line_for('GET', '/issue-templates/fields', 'IssueTemplateFields', auth_wrapper, nl),
        line_for('GET', '/issue-templates', 'IssueTemplatesList', perm_wrapper, nl, 'PermSettingsEdit'),
        line_for('GET', '/issue-templates/new', 'IssueTemplateNew', perm_wrapper, nl, 'PermSettingsEdit'),
        line_for('POST', '/issue-templates', 'IssueTemplateCreate', perm_wrapper, nl, 'PermSettingsEdit'),
        line_for('GET', '/issue-templates/{key}', 'IssueTemplateDetail', perm_wrapper, nl, 'PermSettingsEdit'),
        line_for('GET', '/issue-templates/{key}/edit', 'IssueTemplateEdit', perm_wrapper, nl, 'PermSettingsEdit'),
        line_for('POST', '/issue-templates/{key}/edit', 'IssueTemplateUpdate', perm_wrapper, nl, 'PermSettingsEdit'),
        line_for('POST', '/issue-templates/{key}/assign', 'IssueTemplateAssign', perm_wrapper, nl, 'PermSettingsEdit'),
        line_for('POST', '/issue-templates/{key}/delete', 'IssueTemplateDelete', perm_wrapper, nl, 'PermSettingsEdit'),
    ]
    return lines


def main(path):
    patch_issue_routes(path)
    patch_asset_qr_routes(path)


def patch_issue_routes(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()

    start = s.find('func (r *Router) MountIssuesRoutes')
    if start == -1:
        print('  router: MountIssuesRoutes not found', file=sys.stderr)
        sys.exit(1)

    end = s.find('\nfunc ', start + 10)
    if end == -1:
        end = len(s)

    block = s[start:end]
    reject = find_line_optional(block, 'POST /issues/{id}/reject')
    nl = nl_of(block)

    if reject is None:
        toggle = find_line(block, 'POST /issues/{id}/toggle-status')
        auth_wrapper, perm_wrapper = wrapper_of(toggle.group(0), 'POST /issues/{id}/toggle-status')
        block = replace_line(block, toggle, lifecycle_lines(auth_wrapper, perm_wrapper, nl))
        print('  router: lifecycle and template routes inserted')
        reject = find_line(block, 'POST /issues/{id}/reject')
    else:
        auth_wrapper, perm_wrapper = wrapper_of(reject.group(0), 'POST /issues/{id}/reject')

    if '"GET /issues/sla-estimate"' not in block:
        anchor = find_line(block, 'GET /issues/new')
        block = insert_after(block, anchor, [
            line_for('GET', '/issues/sla-estimate', 'IssueSLAEstimate', auth_wrapper, nl),
        ])
        print('  router: SLA estimate route inserted')

    missing_templates = [
        ('GET', '/issue-templates/fields', 'IssueTemplateFields', auth_wrapper, None),
        ('GET', '/issue-templates', 'IssueTemplatesList', perm_wrapper, 'PermSettingsEdit'),
        ('GET', '/issue-templates/new', 'IssueTemplateNew', perm_wrapper, 'PermSettingsEdit'),
        ('POST', '/issue-templates', 'IssueTemplateCreate', perm_wrapper, 'PermSettingsEdit'),
        ('GET', '/issue-templates/{key}', 'IssueTemplateDetail', perm_wrapper, 'PermSettingsEdit'),
        ('GET', '/issue-templates/{key}/edit', 'IssueTemplateEdit', perm_wrapper, 'PermSettingsEdit'),
        ('POST', '/issue-templates/{key}/edit', 'IssueTemplateUpdate', perm_wrapper, 'PermSettingsEdit'),
        ('POST', '/issue-templates/{key}/assign', 'IssueTemplateAssign', perm_wrapper, 'PermSettingsEdit'),
        ('POST', '/issue-templates/{key}/delete', 'IssueTemplateDelete', perm_wrapper, 'PermSettingsEdit'),
    ]
    additions = []
    for method, route_path, handler, wrapper, perm in missing_templates:
        if f'"{method} {route_path}"' not in block:
            additions.append(line_for(method, route_path, handler, wrapper, nl, perm))

    if '"GET /issues/{id}/assign"' not in block:
        additions.append(line_for('GET', '/issues/{id}/assign', 'IssueAssignFormPage', auth_wrapper, nl))
    if '"POST /issues/{id}/assign"' not in block:
        additions.append(line_for('POST', '/issues/{id}/assign', 'IssueAssign', auth_wrapper, nl))
    if '"GET /issues/group"' not in block:
        additions.append(line_for('GET', '/issues/group', 'IssueGroupFormPage', auth_wrapper, nl))
        additions.append(line_for('POST', '/issues/group', 'IssueGroup', auth_wrapper, nl))
        additions.append(line_for('POST', '/issues/{id}/ungroup', 'IssueUngroup', auth_wrapper, nl))
    if '"POST /issues/{id}/hold"' not in block:
        additions.append(line_for('POST', '/issues/{id}/hold', 'IssueHold', auth_wrapper, nl))
    if '"POST /issues/{id}/resume"' not in block:
        additions.append(line_for('POST', '/issues/{id}/resume', 'IssueResume', auth_wrapper, nl))
    if '"GET /issues/{id}/merge"' not in block:
        additions.append(line_for('GET', '/issues/{id}/merge', 'IssueMergeFormPage', auth_wrapper, nl))
    if '"POST /issues/{id}/merge"' not in block:
        additions.append(line_for('POST', '/issues/{id}/merge', 'IssueMerge', auth_wrapper, nl))

    if additions:
        block = insert_after(block, find_line(block, 'POST /issues/{id}/reject'), additions)
        print('  router: missing issue routes inserted')
    else:
        print('  router: issue routes already present')

    if '"GET /issue-qr-codes"' not in block:
        qr_admin = [
            line_for('GET', '/issue-qr-codes', 'IssueQRCodesList', perm_wrapper, nl, 'PermSettingsEdit'),
            line_for('POST', '/issue-qr-codes/revoke', 'IssueQRCodesBulkRevoke', perm_wrapper, nl, 'PermSettingsEdit'),
            line_for('POST', '/issue-qr-codes/{id}/revoke', 'IssueQRCodeAdminRevoke', perm_wrapper, nl, 'PermSettingsEdit'),
        ]
        template_anchor = find_line_optional(block, 'GET /issue-templates/fields')
        block = insert_after(block, template_anchor or find_line(block, 'POST /issues/{id}/reject'), qr_admin)
        print('  router: issue QR admin routes inserted')

    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s[:start] + block + s[end:])


def patch_asset_qr_routes(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()

    start = s.find('func (r *Router) MountAssetRoutes')
    if start == -1:
        print('  router: MountAssetRoutes not found', file=sys.stderr)
        sys.exit(1)

    end = s.find('\nfunc ', start + 10)
    if end == -1:
        end = len(s)

    block = s[start:end]
    if '"POST /assets/{id}/qr/generate"' in block:
        print('  router: asset QR routes already present')
        return

    asset_detail = find_line(block, 'GET /assets/{id}')
    auth_wrapper, perm_wrapper = wrapper_of(asset_detail.group(0), 'GET /assets/{id}')
    nl = nl_of(block)
    routes = [
        line_for('GET', '/assets/{id}/qr/card', 'AssetQRCodeCard', auth_wrapper, nl),
        line_for('GET', '/assets/{id}/qr/image', 'AssetQRCodeImage', auth_wrapper, nl),
        line_for('POST', '/assets/{id}/qr/image', 'AssetQRCodeRenderImage', perm_wrapper, nl, 'PermAssetsAssetEdit'),
        line_for('POST', '/assets/{id}/qr/generate', 'AssetQRCodeGenerate', perm_wrapper, nl, 'PermAssetsAssetEdit'),
        line_for('POST', '/assets/{id}/qr/regenerate', 'AssetQRCodeRegenerate', perm_wrapper, nl, 'PermAssetsAssetEdit'),
        line_for('POST', '/assets/{id}/qr/revoke', 'AssetQRCodeRevoke', perm_wrapper, nl, 'PermAssetsAssetEdit'),
    ]
    block = insert_after(block, asset_detail, routes)
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s[:start] + block + s[end:])
    print('  router: asset QR routes inserted')


if __name__ == '__main__':
    main(sys.argv[1])
