#!/usr/bin/env python3
"""Register the formengine properties added by the issue overlays."""
import sys


PROPS = [
    '{Key: "x-seam-option-disabled", Title: "Disabled options", Help: "Option values shown but not selectable, one per line.", Kind: KindList, Widgets: []string{"select"}},',
]


def main(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()

    missing = [prop for prop in PROPS if f'Key: "{prop.split(chr(34))[1]}"' not in s]
    if not missing:
        print('  catalogue: issue properties already present')
        return

    anchor = 'var props = []Prop{'
    if anchor not in s:
        print('  catalogue: props declaration not found', file=sys.stderr)
        sys.exit(1)

    nl = '\r\n' if '\r\n' in s else '\n'
    addition = ''.join('\t' + prop + nl for prop in missing)
    s = s.replace(anchor, anchor + nl + addition, 1)
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s)
    print('  catalogue: issue properties inserted')


if __name__ == '__main__':
    main(sys.argv[1])
