#!/usr/bin/env python3
"""Add merge.json to the issues form-schema embed."""
import re
import sys


def main(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()

    match = re.search(r'^//go:embed .*$', s, re.MULTILINE)
    if match is None:
        print('  issues schemas: //go:embed line not found', file=sys.stderr)
        sys.exit(1)
    if 'merge.json' in match.group(0):
        print('  issues schemas: merge.json already embedded')
        return

    line = match.group(0) + ' merge.json'
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s[:match.start()] + line + s[match.end():])
    print('  issues schemas: merge.json embedded')


if __name__ == '__main__':
    main(sys.argv[1])
