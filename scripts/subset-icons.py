#!/usr/bin/env python
"""Subset the Material Symbols Outlined font to exactly the ligatures the app uses.

Why this exists: a naive `pyftsubset --text` on icon fonts explodes via
class-based GSUB closure (thousands of glyphs survive). This script instead
walks the GSUB ligature lookups (through type-7 Extension wrappers), finds
the rules matching the app's icon ligature names, and subsets with
layout_closure disabled, keeping only those rules' components + targets.

Usage:
    1. Obtain a full Material Symbols Outlined woff2 (e.g. Google css2 with a
       modern browser User-Agent and &text=<ligatures>, or the official
       variablefont from google/material-design-icons).
    2. python scripts/subset-icons.py <input.woff2> web/fonts/material-symbols-outlined.woff2
    3. Update ICON_LIGATURES below to match every icon name used in
       web/index.html (`<md-icon>NAME</md-icon>`) and web/app.js (icon
       textContent strings), then rerun after UI changes.

Requires: fonttools[brotli] (pip install fonttools brotli).
"""
import sys
from fontTools.ttLib import TTFont
from fontTools import subset

# Every icon ligature currently used by the app (scan: md-icon contents +
# icon textContent assignments in app.js). Adding a NEW icon to the UI means
# adding its name here and regenerating the subset.
ICON_LIGATURES = (
    "arrow_back arrow_downward arrow_upward autorenew bolt check_circle "
    "chevron_right close cloud_off code contrast crop_square delete_sweep "
    "description donut_small download error expand_more feedback info key "
    "keyboard lan language light_mode link memory menu_book monitoring "
    "open_in_new palette power_settings_new public receipt_long refresh "
    "remove settings swap_horiz system_update terminal toggle_on "
    "trending_up visibility_off warning wifi_tethering"
).split()


def main(src: str, out: str) -> None:
    words = ICON_LIGATURES
    chars = sorted({c for w in words for c in w})

    font = TTFont(src)
    cmap = font.getBestCmap()
    inv = {cmap[ord(c)]: c for c in chars if ord(c) in cmap}

    target_names: set[str] = set()
    found: set[str] = set()
    gsub = font["GSUB"].table
    for lookup in gsub.LookupList.Lookup:
        subtables = []
        if lookup.LookupType == 4:
            subtables = list(lookup.SubTable)
        elif lookup.LookupType == 7:  # Extension → real type behind it
            for st in lookup.SubTable:
                if getattr(st, "ExtensionLookupType", 0) == 4:
                    subtables.append(st.ExtSubTable)
        for st in subtables:
            for first, liglist in st.ligatures.items():
                for lig in liglist:
                    seq = [first] + list(lig.Component)
                    word = ""
                    ok = True
                    for glyph_name in seq:
                        if glyph_name in inv:
                            word += inv[glyph_name]
                        else:
                            ok = False
                            break
                    if ok and word in words:
                        found.add(word)
                        target_names.add(lig.LigGlyph)
    missing = sorted(set(words) - found)
    print(f"ligature rules matched: {len(found)}/{len(words)}")
    if missing:
        print("MISSING (update ICON_LIGATURES or check the input font):", missing)
        sys.exit(1)

    options = subset.Options()
    options.layout_features = ["*"]      # keep rlig/calt intact
    options.layout_closure = False       # no class-closure explosion
    options.hinting = False
    options.flavor = "woff2"
    options.notdef_outline = True

    subsetter = subset.Subsetter(options=options)
    subsetter.populate(text="".join(chars), glyphs=sorted(target_names))
    subsetter.subset(font)
    subset.save_font(font, out, options)

    size = __import__("os").path.getsize(out)
    print(f"wrote {out} ({size / 1024:.0f} KB)")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(__doc__)
        sys.exit(2)
    main(sys.argv[1], sys.argv[2])
