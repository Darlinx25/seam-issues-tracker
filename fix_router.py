#!/usr/bin/env python3
"""Insert the assign routes into MountIssuesRoutes.

Idempotent: if the routes are already registered the file is left untouched.
Only the two assign lines are added, right after the reject routes, so
upstream changes elsewhere in the block are preserved.
"""
import sys

ASSIGN_ROUTES = [
    '\tmux.HandleFunc("GET /issues/{id}/assign", r.requireAuth(r.handler.IssueAssignFormPage))\n',
    '\tmux.HandleFunc("POST /issues/{id}/assign", r.requireAuth(r.handler.IssueAssign))\n',
]

REJECT_ROUTE = '\tmux.HandleFunc("POST /issues/{id}/reject", r.requireAuth(r.handler.IssueReject))\n'


def main(path):
    with open(path, 'r', encoding='utf-8') as f:
        s = f.read()

    if '"GET /issues/{id}/assign"' in s and '"POST /issues/{id}/assign"' in s:
        print("  router: assign routes already present")
        return

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

    block = block.replace(REJECT_ROUTE, REJECT_ROUTE + ''.join(ASSIGN_ROUTES), 1)
    s = s[:start] + block + s[end:]

    with open(path, 'w', encoding='utf-8') as f:
        f.write(s)
    print("  router: assign routes inserted")


if __name__ == '__main__':
    main(sys.argv[1])
