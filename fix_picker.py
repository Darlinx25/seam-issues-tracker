#!/usr/bin/env python3
"""Add the issues picker search config to asset_picker_search.go."""
import sys


ISSUES_ENTRY = '''\t"entity:issues": {
\t\ttable:        "issues",
\t\tsearchColumn: "title",
\t\tlabel: func(row map[string]any) string {
\t\t\treturn "#" + strconv.FormatInt(toInt64(row["id"]), 10) + " — " + toString(row["title"])
\t\t},
\t},
'''


def fail(message):
    print(f'  picker: {message}', file=sys.stderr)
    sys.exit(1)


def main(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        s = f.read()

    if '"entity:issues"' in s:
        print('  picker: issues entity already present')
        return

    nl = '\r\n' if '\r\n' in s else '\n'
    lines = s.splitlines(keepends=True)

    if '"strconv"' not in s:
        for i, line in enumerate(lines):
            if line.strip() == '"net/http"':
                lines.insert(i + 1, '\t"strconv"' + nl)
                break
        else:
            fail('cannot add strconv import')
        print('  picker: strconv import added')

    start = next((i for i, line in enumerate(lines) if line.startswith('var pickerSearchConfigs ')), None)
    if start is None:
        fail('pickerSearchConfigs not found')

    type_index = next((i for i, line in enumerate(lines) if line.startswith('type pickerSearchResult ')), None)
    if type_index is None:
        fail('pickerSearchResult type not found')
    close = next((i for i in range(type_index - 1, start, -1) if lines[i].rstrip('\r\n') == '}'), None)
    if close is None:
        fail('pickerSearchConfigs closing brace not found')

    entry = ISSUES_ENTRY.replace('\n', nl).splitlines(keepends=True)
    lines[close:close] = entry

    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.writelines(lines)
    print('  picker: issues entity inserted')


if __name__ == '__main__':
    main(sys.argv[1])
