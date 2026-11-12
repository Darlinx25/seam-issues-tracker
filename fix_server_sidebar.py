#!/usr/bin/env python3
"""Ensure Issues sidebar includes QR codes admin item. Idempotent."""
import sys

ANCHOR = '				{Label: "issues.templates", URL: "/issue-templates", Icon: "article", RequiredPerm: string(auth.PermSettingsEdit)},'
ITEM = '				{Label: "issue_qr_admin.nav", URL: "/issue-qr-codes", Icon: "qr_code_2", RequiredPerm: string(auth.PermSettingsEdit)},'

def main(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()
    if '"issue_qr_admin.nav"' in s or '/issue-qr-codes' in s and 'SidebarItems' in s.split('/issue-qr-codes')[0][-200:]:
        print('  server: QR sidebar item already present')
        return
    if ANCHOR not in s:
        print('  server: issues templates sidebar anchor not found, skipping')
        return
    s = s.replace(ANCHOR, ANCHOR + '\n' + ITEM, 1)
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s)
    print('  server: QR sidebar item inserted')

if __name__ == '__main__':
    main(sys.argv[1])
