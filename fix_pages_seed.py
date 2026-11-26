#!/usr/bin/env python3
"""Register the issues/merge page in the developer pages seed."""
import json
import sys


def main(path):
    with open(path, 'r', encoding='utf-8') as f:
        data = json.load(f)

    if 'issues/merge' in data:
        print('  pages seed: issues/merge already present')
        return

    data['issues/merge'] = {'Module': 'issues', 'Form': 'merge'}
    with open(path, 'w', encoding='utf-8') as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
        f.write('\n')
    print('  pages seed: issues/merge registered')


if __name__ == '__main__':
    main(sys.argv[1])
