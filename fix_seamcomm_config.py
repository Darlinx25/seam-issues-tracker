#!/usr/bin/env python3
"""Wire the seam-comm notification client into the Seam configuration chain.

Issues can opt in to state-change notifications. The companion service is
separate from Seam, so three upstream files need two new values and the example
config needs to document them:

  internal/config/config.go     features.comms_url / features.comms_api_key
  internal/ui/layout.go         ui.Config gains CommsURL / CommsAPIKey
  internal/server/server.go     copies both into the handler config
  config.example.json           working local defaults (seam-comm on :8091;
                                port 8090 is taken by the QR image renderer)

An empty comms_url disables notifications, so a merge still builds and runs
without seam-comm installed.

The anchors are added by fix_qr_config.py on older bases, so this fixer must
run after it. Idempotent: each hook is skipped once present. If an anchor is
missing, the merge fails loudly rather than shipping a build that silently
ignores the notification settings.
"""
import sys


CONFIG_ANCHOR = '\tQRRenderURL       string `json:"qr_render_url"` // 2D-code renderer base URL (empty = no QR images)\n'
CONFIG_NEW = (
    CONFIG_ANCHOR +
    '\tCommsURL          string `json:"comms_url"`      // seam-comm base URL (empty = state notifications disabled)\n'
    '\tCommsAPIKey       string `json:"comms_api_key"`   // API key sent to seam-comm as a Bearer token\n'
)

LAYOUT_ANCHOR = ('\tQRRenderURL       string              '
                 '// QRRenderURL is the base URL of the 2D-code renderer (empty = no QR images)\n')
LAYOUT_NEW = (
    LAYOUT_ANCHOR +
    '\tCommsURL          string              '
    '// CommsURL is the base URL of seam-comm (empty = state notifications disabled)\n'
    '\tCommsAPIKey       string              '
    '// CommsAPIKey is the tenant API key used when talking to seam-comm\n'
)

SERVER_ANCHOR = '\t\tuiCfg.QRRenderURL = s.cfg.Features.QRRenderURL\n'
SERVER_NEW = (
    SERVER_ANCHOR +
    '\t\tuiCfg.CommsURL = s.cfg.Features.CommsURL\n'
    '\t\tuiCfg.CommsAPIKey = s.cfg.Features.CommsAPIKey\n'
)

EXAMPLE_ANCHOR = '"qr_render_url":'
EXAMPLE_NEW = (
    '    "comms_url": "http://127.0.0.1:8091",\n'
    '    "comms_api_key": "dev-key",\n'
)


def newline(s):
    return '\r\n' if '\r\n' in s else '\n'


def read(path):
    with open(path, 'r', encoding='utf-8', newline='') as f:
        return f.read()


def write(path, s):
    with open(path, 'w', encoding='utf-8', newline='') as f:
        f.write(s)


def replace_once(path, anchor, replacement, marker, label):
    s = read(path)
    if marker in s:
        print('  %s: already wired' % label)
        return
    nl = newline(s)
    a, r = anchor.replace('\n', nl), replacement.replace('\n', nl)
    if a not in s:
        raise SystemExit('  %s: anchor not found -- refusing to write a partial config' % label)
    write(path, s.replace(a, r, 1))
    print('  %s: wired' % label)


def main():
    if len(sys.argv) != 5:
        raise SystemExit('usage: fix_seamcomm_config.py <config.go> <layout.go> <server.go> <config.example.json>')
    config_go, layout_go, server_go, example = sys.argv[1:]
    replace_once(config_go, CONFIG_ANCHOR, CONFIG_NEW, 'CommsURL', 'config.go')
    replace_once(layout_go, LAYOUT_ANCHOR, LAYOUT_NEW, 'CommsURL', 'layout.go')
    replace_once(server_go, SERVER_ANCHOR, SERVER_NEW, 'uiCfg.CommsURL', 'server.go')

    s = read(example)
    if '"comms_url"' in s:
        print('  config.example.json: already wired')
        return
    lines = s.splitlines(keepends=True)
    out, inserted = [], False
    for line in lines:
        out.append(line)
        if not inserted and EXAMPLE_ANCHOR in line:
            nl = newline(s)
            out.append(EXAMPLE_NEW.replace('\n', nl))
            inserted = True
    if not inserted:
        raise SystemExit('  config.example.json: anchor not found -- refusing to write a partial config')
    write(example, ''.join(out))
    print('  config.example.json: comms defaults set to :8091 / dev-key')


if __name__ == '__main__':
    main()
