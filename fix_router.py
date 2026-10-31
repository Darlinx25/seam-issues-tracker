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


def main(path):
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


if __name__ == '__main__':
    main(sys.argv[1])