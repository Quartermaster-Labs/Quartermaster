#!/usr/bin/env python3
"""Derive every quartermaster icon asset from the vector master.

Run from the repo root after changing mark.py:

    python packaging/icons/gen.py
    make versioninfo versioninfo-setup   # re-embed favicon.ico into both exes
    make ui                              # regenerate internal/server/ui_dist

The .site pages are rewritten in place, so re-run this after adding a page and
commit whatever it touches. GitHub itself takes no icon from the repo: what
shows next to the repo name is the owner avatar, uploaded in org settings, for
which icon.png below is the file.

The master is mark.py, not an image: the wheel is declared as geometry and both
the PNGs and the SVGs are emitted from it, so nothing here is ever upscaled and
favicon.svg is a real vector rather than a PNG in an <image> wrapper. Every
raster size below is drawn at its own size rather than downsampled from 256, so
a 16px frame gets its own antialiasing pass instead of a blurred 256.

Which surfaces get the plate, and why (see mark.py for the two forms):

  plated   the .exe resource / tray / installer icon, apple-touch-icon, and the
           Android maskable manifest icons. All four sit on a background the app
           does not control, and two of them (iOS flattens onto black, Android
           circle-crops) cannot show transparency at all.
  naked    the in-app title bar mark, the browser tab, and the GitHub Pages
           site (its favicon and the inline header mark on every page). All
           already sit on our own chrome, where a plate would read as a sticker.

Because the plate is opaque to its own edges it is drawn full-bleed: Windows
sizes a taskbar icon by its opaque extent, so canvas padding would make the app
read smaller than its neighbours. The wheel's breathing room is the plate's
inset, inside the artwork, never on the canvas.

icon.png is no longer an input. It is now an OUTPUT: the 512px plated master,
for anywhere a plain image of the app icon is wanted. (The v3 bicorne art it
replaced is in git history, not in the tree.)

Needs: pip install pillow brotli
"""

import gzip
import io
import os
import sys

from PIL import Image

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import mark

try:
    import brotli
except ImportError:
    brotli = None

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
PUBLIC = os.path.join(ROOT, "ui-svelte", "public")
ASSETS = os.path.join(ROOT, "ui-svelte", "src", "assets")
UI_DIST = os.path.join(ROOT, "internal", "server", "ui_dist")
# The GitHub Pages site's SOURCE. .site/ at the repo root is gitignored build
# output that ui-svelte/site/build.mjs regenerates and pages.yml deploys, so
# writing there would be undone by the next build: the mark goes into the
# source, and build.mjs reads these two files for its <link rel=icon> and its
# inline header mark.
SITE_SRC = os.path.join(ROOT, "ui-svelte", "site")
SITE_BUILD = os.path.join(ROOT, ".site")

# The .exe resource is looked up by exact metric: nativewin/icon_windows.go asks
# for SM_CXICON and SM_CXSMICON separately so Windows picks a frame instead of
# rescaling the 256 one. Every size Windows actually asks for needs to be here.
ICO_SIZES = (16, 20, 24, 32, 48, 64, 256)


def site_font(weight, size):
    """The site's own Inter, at a given weight, as a Pillow font.

    The site ships Inter as woff2 only, which FreeType will not open, so it is
    converted through fontTools. That keeps the card reproducible from repo
    assets: no dependence on whatever happens to be installed on the machine
    that runs this. Returns None if fontTools is missing, which is the whole
    reason the card is optional.
    """
    try:
        from fontTools.ttLib import TTFont
    except ImportError:
        return None
    # build.mjs copies these out of @fontsource into the built site; read
    # whichever exists, so the card builds from a checkout that has had either
    # an npm install or a site build.
    for src in (os.path.join(ROOT, "ui-svelte", "node_modules", "@fontsource",
                             "inter", "files", f"inter-latin-{weight}-normal.woff2"),
                os.path.join(SITE_BUILD, "assets", "fonts", f"inter-{weight}.woff2")):
        if os.path.exists(src):
            break
    else:
        return None
    buf = io.BytesIO()
    f = TTFont(src)
    f.flavor = None                      # woff2 -> plain ttf, in memory
    f.save(buf)
    buf.seek(0)
    from PIL import ImageFont
    return ImageFont.truetype(buf, size)


def social_card():
    """The 1280x640 card GitHub shows when the repo is linked somewhere.

    This is the only per-repo image GitHub hosts: the avatar beside the repo
    name belongs to the owner account, not the repo. Upload it under
    Settings -> General -> Social preview.
    """
    from PIL import ImageDraw

    bold, body = site_font(700, 84), site_font(400, 30)
    if not bold or not body:
        return None                      # pip install fonttools to regenerate

    W, H = 1280, 640
    a, b = mark._rgb(mark.PLATE_FROM), mark._rgb(mark.PLATE_TO)
    card = Image.new("RGB", (W, H))
    px = card.load()
    span = (W - 1) + (H - 1)
    for y in range(H):
        for x in range(W):
            t = (x + y) / span
            px[x, y] = tuple(int(a[i] + (b[i] - a[i]) * t) for i in range(3))

    d = ImageDraw.Draw(card)
    title, tag = "quartermaster", "Run any model on your own machine."
    # Lay the lockup out from the type's real ink extents rather than from the
    # nominal line heights: Inter's ascent leaves enough slack above the
    # lowercase that a nominal centring visibly rides high.
    tb, gb = d.textbbox((0, 0), title, font=bold), d.textbbox((0, 0), tag, font=body)
    gap = 26
    block = (tb[3] - tb[1]) + gap + (gb[3] - gb[1])
    top = (H - block) // 2

    side = 300
    art = mark.render(side)
    card.paste(art, (150, (H - side) // 2), art)

    tx = 530
    d.text((tx - tb[0], top - tb[1]), title, font=bold, fill=(237, 237, 238))
    d.text((tx - gb[0], top + (tb[3] - tb[1]) + gap - gb[1]), tag, font=body,
           fill=(161, 161, 166))
    return card


def png_bytes(img):
    buf = io.BytesIO()
    img.save(buf, "PNG", optimize=True)
    return buf.getvalue()


def ico_bytes(draw):
    """An .ico whose every frame was drawn at its own size, not downsampled."""
    frames = [draw(n) for n in ICO_SIZES]
    buf = io.BytesIO()
    frames[-1].save(buf, "ICO", sizes=[(n, n) for n in ICO_SIZES],
                    append_images=frames[:-1])
    return buf.getvalue()


def main():
    naked, plated = mark.render, mark.render_plated

    files = {
        # repo root: goversioninfo -icon, tray_windows.go //go:embed, and
        # installer.iss SetupIconFile all read this one file. Plated: it lands
        # on the taskbar, the tray, and the desktop.
        os.path.join(ROOT, "favicon.ico"): ico_bytes(plated),
        os.path.join(ROOT, "icon.png"): png_bytes(plated(512)),
        # The browser tab: naked, and served three ways because index.html links
        # all three and browsers disagree about which they prefer.
        os.path.join(PUBLIC, "favicon.ico"): ico_bytes(naked),
        os.path.join(PUBLIC, "favicon.svg"): mark.svg().encode(),
        os.path.join(PUBLIC, "favicon-96x96.png"): png_bytes(naked(96)),
        # Bundled (not public/), because the UI is served under a base path and
        # an import gets a hashed, base-correct URL. 64px: it renders at 16 in
        # the title bar, with headroom for a 2x display and the interface-size
        # zoom on top of that.
        os.path.join(ASSETS, "mark.png"): png_bytes(naked(64)),
        os.path.join(PUBLIC, "apple-touch-icon.png"): png_bytes(plated(180)),
        os.path.join(PUBLIC, "web-app-manifest-192x192.png"): png_bytes(plated(192)),
        os.path.join(PUBLIC, "web-app-manifest-512x512.png"): png_bytes(plated(512)),
        # The two masters as vectors, for anything that wants to edit or print
        # them. Generated, like everything else: mark.py stays the source.
        os.path.join(HERE, "mark.svg"): mark.svg().encode(),
        os.path.join(HERE, "mark-plated.svg"): mark.svg_plated().encode(),
        # Upload-sized, for anywhere a person has to hand a file to a web form:
        # the GitHub owner avatar, a package registry, a directory listing.
        # Naked and transparent, so it takes the host page's own ground.
        os.path.join(HERE, "mark-512.png"): png_bytes(mark.render(512)),
    }

    # The site: its favicon, and its inline header mark. Naked, because the
    # site header has its own ground and the site's accent is already this
    # orange. The header copy is drawn in currentColor plus one variable, so the
    # light theme's deliberately darker accent applies to the mark too.
    if os.path.isdir(SITE_SRC):
        files[os.path.join(SITE_SRC, "mark.svg")] = mark.svg().encode()
        files[os.path.join(SITE_SRC, "mark-inline.svg")] = mark.svg_inline(
            22, 'data-mark="brand" aria-hidden="true"',
            {mark.O_BRAND: "currentColor", mark.O_DEEP: "var(--mark-deep)"}).encode()
        card = social_card()
        if card is not None:
            files[os.path.join(HERE, "social-preview.png")] = png_bytes(card)
        else:
            print("  (no fontTools; social-preview.png left as is)", file=sys.stderr)

    # The README's own mark, above the dashboard shot.
    files[os.path.join(ROOT, "docs", "assets", "mark.png")] = png_bytes(mark.render(128))

    for path, data in files.items():
        with open(path, "wb") as fh:
            fh.write(data)
        print(f"{os.path.relpath(path, ROOT):48} {len(data):>7}")

    # ui_dist is gitignored Vite output, but mirroring it means the checkout
    # serves the new icon without a rebuild. Vite copies public/ verbatim, so
    # these are the exact bytes `make ui` would produce; the .br/.gz siblings
    # exist because server/ui.go prefers them.
    if not os.path.isdir(UI_DIST):
        return
    for name in os.listdir(PUBLIC):
        dst = os.path.join(UI_DIST, name)
        if not os.path.exists(dst):
            continue
        data = open(os.path.join(PUBLIC, name), "rb").read()
        open(dst, "wb").write(data)
        if os.path.exists(dst + ".gz"):
            open(dst + ".gz", "wb").write(gzip.compress(data, 9))
        if os.path.exists(dst + ".br"):
            if brotli is None:
                print("  (no brotli module; %s.br left stale)" % name, file=sys.stderr)
            else:
                open(dst + ".br", "wb").write(brotli.compress(data, quality=11))
        print(f"  mirrored -> {os.path.relpath(dst, ROOT)}")


if __name__ == "__main__":
    main()
