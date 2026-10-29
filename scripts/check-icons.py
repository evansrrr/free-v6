#!/usr/bin/env python
"""Fail the build when a UI icon can't actually be rendered.

The app draws its icons from a *subset* font (web/fonts/material-symbols-outlined.woff2)
that only carries the ligatures listed in scripts/subset-icons.py. Adding
`<md-icon>some_name</md-icon>` without adding `some_name` to that list doesn't
error — the browser falls back to the plain text, which the 16px icon box then
crops into a letter fragment ("blur_on" rendered as "R", "image" as "A").

That shipped once. This script makes it impossible to ship silently: it collects
every font-based icon name from the UI and checks it against the subset.

Icons built as inline <svg> are exempt by construction — they carry their own
paths and never touch the font.

Usage:
    python scripts/check-icons.py            # check, exit 1 on drift
    python scripts/check-icons.py --verbose  # also list every name found
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
HTML = ROOT / "web" / "index.html"
APP_JS = ROOT / "web" / "app.js"
SUBSET_SCRIPT = ROOT / "scripts" / "subset-icons.py"

# Matches the ligature list literal inside subset-icons.py:
#   ICON_LIGATURES = ("a b c " "d e f").split()
LIST_LITERAL = re.compile(r"ICON_LIGATURES\s*=\s*\((.*?)\)\.split\(\)", re.S)

# <md-icon>name</md-icon> — plain text child means "render a ligature".
MD_ICON_TEXT = re.compile(r"<md-icon\b[^>]*>\s*([a-z][a-z0-9_]*)\s*</md-icon>")

# Icons swapped at runtime, e.g. `maxIcon.textContent = 'filter_none'`.
# Scoped to identifiers that look like icon handles so we don't sweep up
# unrelated label strings.
ICON_ASSIGN = re.compile(
    r"\b\w*[Ii]con\w*\.textContent\s*=\s*([^;]+);"
)
QUOTED = re.compile(r"['\"]([a-z][a-z0-9_]*)['\"]")


def subset_names() -> set[str]:
    """The ligature names the subset font was built from."""
    src = SUBSET_SCRIPT.read_text(encoding="utf-8")
    m = LIST_LITERAL.search(src)
    if not m:
        sys.exit("check-icons: could not find ICON_LIGATURES in subset-icons.py")
    return set(re.findall(r"[a-z][a-z0-9_]*", m.group(1)))


def used_names() -> dict[str, list[str]]:
    """Font icon names referenced by the UI, mapped to where they came from."""
    found: dict[str, list[str]] = {}

    html = HTML.read_text(encoding="utf-8")
    for name in MD_ICON_TEXT.findall(html):
        found.setdefault(name, []).append("index.html")

    js = APP_JS.read_text(encoding="utf-8")
    for line_no, line in enumerate(js.splitlines(), 1):
        for expr in ICON_ASSIGN.findall(line):
            for name in QUOTED.findall(expr):
                found.setdefault(name, []).append(f"app.js:{line_no}")

    return found


def main() -> int:
    verbose = "--verbose" in sys.argv
    have = subset_names()
    used = used_names()

    missing = {n: w for n, w in used.items() if n not in have}

    if verbose:
        print(f"subset glyphs : {len(have)}")
        print(f"icons in use  : {len(used)}")
        for name in sorted(used):
            mark = "ok  " if name in have else "MISS"
            print(f"  {mark} {name:<20} {', '.join(sorted(set(used[name])))}")

    if missing:
        print("check-icons: these icon names are not in the font subset and would")
        print("             render as letter fragments:\n")
        for name, where in sorted(missing.items()):
            print(f"  {name:<20} {', '.join(sorted(set(where)))}")
        print(
            "\nFix by either:\n"
            "  1. adding the name to ICON_LIGATURES in scripts/subset-icons.py and\n"
            "     regenerating web/fonts/material-symbols-outlined.woff2, or\n"
            "  2. drawing it as an inline <svg> inside <md-icon> (see the\n"
            "     setting-icon icons in web/index.html)."
        )
        return 1

    print(f"check-icons: ok — {len(used)} icons, all present in the subset")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
