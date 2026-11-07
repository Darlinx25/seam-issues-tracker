#!/usr/bin/env python3
"""Wire the QR image settings into the configuration chain.

The per-asset QR images need two values that the base configuration does not
carry yet, and both live in upstream files the overlay cannot reach:

  internal/config/config.go     features.qr_render_url: the base URL of the
                                2D-code renderer (empty = images disabled).
  internal/ui/layout.go         ui.Config gains QRBaseURL and QRRenderURL, the
                                two values the QR card reads at render time.
  internal/server/server.go     copies both from the feature config into the
                                UI config the handlers receive.
  config.example.json           documents the new key next to qr_base_url, and
                                both keys ship with working local defaults
                                (Seam on localhost:8080, renderer on
                                127.0.0.1:8090) so that a fresh merge plus
                                "cp config.example.json config.json" runs the
                                QR image out of the box. The upstream example
                                points qr_base_url at a placeholder host, which
                                would be printed on every generated label.

features.qr_base_url already exists upstream and keeps its original meaning
("base + code", its documented use in the legacy QR generator); it is now also
what the per-asset codes encode, so a printed label points at the public host
instead of an in-app relative path.

Idempotent: each hook is skipped once it is already present. If an anchor is
missing, the merge fails loudly rather than shipping a build where the QR image
settings are silently ignored.
"""
import re
import sys

CONFIG_OLD = '\tQRBaseURL         string `json:"qr_base_url"`\n'
CONFIG_NEW = (
    '\tQRBaseURL         string `json:"qr_base_url"`\n'
    '\tQRRenderURL       string `json:"qr_render_url"` // 2D-code renderer base URL (empty = no QR images)\n'
)

LAYOUT_OLD = ('\tSeamAPIURL        string              '
              '// SeamAPIURL is the base URL of seam-api for runtime state lookups (empty = disabled)\n')
LAYOUT_NEW = (
    LAYOUT_OLD +
    '\tQRBaseURL         string              '
    '// QRBaseURL is the public prefix the per-asset QR codes encode (empty = in-app /s/{code})\n'
    '\tQRRenderURL       string              '
    '// QRRenderURL is the base URL of the 2D-code renderer (empty = no QR images)\n'
)

SERVER_OLD = '\t\tuiCfg.SeamAPIURL = s.cfg.Features.SeamAPIURL\n'
SERVER_NEW = (
    SERVER_OLD +
    '\t\tuiCfg.QRBaseURL = s.cfg.Features.QRBaseURL\n'
    '\t\tuiCfg.QRRenderURL = s.cfg.Features.QRRenderURL\n'
)

EXAMPLE_BASE_KEY = '"qr_base_url"'
EXAMPLE_RENDER_KEY = '"qr_render_url"'
# The dev defaults a fresh merge ships with. qr_base_url is the host that ends
# up printed on the label, so it is the one value an operator must change for a
# deployment where phones reach the host by IP instead of by name.
EXAMPLE_BASE_VALUE = "http://localhost:8080"
EXAMPLE_RENDER_VALUE = "http://127.0.0.1:8090"


def _newline(s):
    return '\r\n' if '\r\n' in s else '\n'


def _read(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        return f.read()


def _write(path, s):
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s)


def _replace_once(path, s, old, new, label):
    if new.strip() and new.strip() in s:
        print("  %s: already wired" % label)
        return True
    nl_fixed = old.replace('\n', _newline(s))
    if nl_fixed not in s:
        print("  %s: anchor not found" % label, file=sys.stderr)
        return False
    return _write_replace(path, s, nl_fixed, new.replace('\n', _newline(s)), label)


def _write_replace(path, s, old, new, label):
    _write(path, s.replace(old, new, 1))
    print("  %s: wired" % label)
    return True


def patch_config(path):
    s = _read(path)
    if 'qr_render_url' in s:
        print("  config: features.qr_render_url already present")
        return True
    return _replace_once(path, s, CONFIG_OLD, CONFIG_NEW, 'config')


def patch_layout(path):
    s = _read(path)
    if 'QRRenderURL' in s:
        print("  layout: QRBaseURL/QRRenderURL already present")
        return True
    return _replace_once(path, s, LAYOUT_OLD, LAYOUT_NEW, 'layout')


def patch_server(path):
    s = _read(path)
    if 'uiCfg.QRRenderURL' in s:
        print("  server: QR settings already copied into the UI config")
        return True
    return _replace_once(path, s, SERVER_OLD, SERVER_NEW, 'server')


def _example_key_index(lines, key):
    """Index of the line that defines key in the example file, or -1."""
    pattern = re.compile(r'^\s*' + re.escape(key) + r'\s*:')
    for i, line in enumerate(lines):
        if pattern.match(line):
            return i
    return -1


def _example_value(line):
    """The quoted value of a `"key": "value"` line, or None."""
    m = re.search(r':\s*"([^"]*)"', line)
    return m.group(1) if m else None


def _example_set_value(lines, index, key, value):
    """Rewrite one line's value, keeping its indentation and trailing comma."""
    indent = re.match(r'^[ \t]*', lines[index]).group(0)
    comma = ',' if lines[index].rstrip().endswith(',') else ''
    lines[index] = indent + key + ': "' + value + '"' + comma


def patch_example(path):
    s = _read(path)
    nl = _newline(s)
    lines = s.split(nl)

    base = _example_key_index(lines, EXAMPLE_BASE_KEY)
    if base < 0:
        # The example file is documentation only; a missing qr_base_url is not
        # worth failing the merge over, but the operator should know the local
        # defaults were NOT applied.
        print("  config.example.json: qr_base_url anchor not found "
              "(local defaults NOT applied)", file=sys.stderr)
        return True

    changed = False

    if _example_value(lines[base]) != EXAMPLE_BASE_VALUE:
        _example_set_value(lines, base, EXAMPLE_BASE_KEY, EXAMPLE_BASE_VALUE)
        changed = True
        print("  config.example.json: qr_base_url set to %r"
              % EXAMPLE_BASE_VALUE)

    render = _example_key_index(lines, EXAMPLE_RENDER_KEY)
    if render < 0:
        if not lines[base].rstrip().endswith(','):
            lines[base] = lines[base].rstrip() + ','
        indent = re.match(r'^[ \t]*', lines[base]).group(0)
        lines.insert(base + 1, indent + EXAMPLE_RENDER_KEY + ': "' +
                     EXAMPLE_RENDER_VALUE + '",')
        changed = True
        print("  config.example.json: qr_render_url documented")
    elif _example_value(lines[render]) != EXAMPLE_RENDER_VALUE:
        _example_set_value(lines, render, EXAMPLE_RENDER_KEY,
                           EXAMPLE_RENDER_VALUE)
        changed = True
        print("  config.example.json: qr_render_url set to %r"
              % EXAMPLE_RENDER_VALUE)

    if not changed:
        print("  config.example.json: already ships the local QR defaults")
        return True

    _write(path, nl.join(lines))
    return True


def main(config_path, layout_path, server_path, example_path):
    ok = patch_config(config_path)
    ok = patch_layout(layout_path) and ok
    ok = patch_server(server_path) and ok
    ok = patch_example(example_path) and ok
    if not ok:
        sys.exit(1)


if __name__ == '__main__':
    main(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4])
