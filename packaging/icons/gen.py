#!/usr/bin/env python3
"""Derive every quartermaster icon asset from the master art.

Run from the repo root after changing icon.png:

    python packaging/icons/gen.py
    make versioninfo versioninfo-setup   # re-embed favicon.ico into both exes
    make ui                              # regenerate internal/server/ui_dist

icon.png is the master: the bare bicorne on transparency, square-padded, with NO
empty margin.
Windows and the browser both size an icon by its opaque extent, so any padding
baked into the master makes the app look smaller than its neighbours in the
taskbar -- add breathing room in the artwork, never on the canvas.

icon-original.png is the untouched v3 art: the same hat inside a dark rounded
plate. The subject filled only ~51% of that plate by area, so next to a
full-bleed neighbour in the taskbar the app read as half the size. The plate was
dropped rather than shrunk. Keep the original for provenance, derive from
icon.png.

Because the master is transparent, PLATE below backs the two surfaces that
cannot show transparency sensibly: iOS composites apple-touch-icon onto black,
and an Android maskable icon is circle-cropped and needs to be full-bleed.

Needs: pip install pillow brotli
"""

import gzip
import io
import os
import sys

from PIL import Image

try:
    import brotli
except ImportError:
    brotli = None

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
PUBLIC = os.path.join(ROOT, "ui-svelte", "public")
UI_DIST = os.path.join(ROOT, "internal", "server", "ui_dist")

# The .exe resource is looked up by exact metric: nativewin/icon_windows.go asks
# for SM_CXICON and SM_CXSMICON separately so Windows picks a frame instead of
# rescaling the 256 one. Every size Windows actually asks for needs to be here.
ICO_SIZES = (16, 20, 24, 32, 48, 64, 256)

# The dropped plate's colour, reused wherever a background is mandatory.
PLATE = (37, 38, 40)


def load_master():
    im = Image.open(os.path.join(ROOT, "icon.png")).convert("RGBA")
    bbox = im.getchannel("A").getbbox()
    if bbox != (0, 0, im.width, im.height):
        im = im.crop(bbox)  # a master should not have padding, but never trust it
    side = max(im.size)
    square = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    square.paste(im, ((side - im.width) // 2, (side - im.height) // 2), im)
    return square


def png_bytes(img):
    buf = io.BytesIO()
    img.save(buf, "PNG", optimize=True)
    return buf.getvalue()


def on_plate(master, n, fill=1.0):
    """Composite the master onto an opaque plate at `fill` of the canvas.

    Used for the two surfaces that cannot take transparency: iOS flattens
    apple-touch-icon onto black, and an Android `purpose: maskable` icon is
    circle-cropped, so it must be full-bleed with the subject inside the ~80%
    safe zone.
    """
    out = Image.new("RGBA", (n, n), PLATE + (255,))
    inner = int(n * fill)
    art = master.resize((inner, inner), Image.LANCZOS)
    out.paste(art, ((n - inner) // 2, (n - inner) // 2), art)
    return out


def main():
    master = load_master()
    rs = lambda n: master.resize((n, n), Image.LANCZOS)

    buf = io.BytesIO()
    rs(256).save(buf, "ICO", sizes=[(n, n) for n in ICO_SIZES])
    ico = buf.getvalue()

    files = {
        # repo root: goversioninfo -icon, tray_windows.go //go:embed,
        # and installer.iss SetupIconFile all read this one file.
        os.path.join(ROOT, "favicon.ico"): ico,
        os.path.join(PUBLIC, "favicon.ico"): ico,
        os.path.join(PUBLIC, "favicon-96x96.png"): png_bytes(rs(96)),
        os.path.join(PUBLIC, "apple-touch-icon.png"): png_bytes(on_plate(master, 180, 0.86)),
        os.path.join(PUBLIC, "web-app-manifest-192x192.png"): png_bytes(on_plate(master, 192, 0.78)),
        os.path.join(PUBLIC, "web-app-manifest-512x512.png"): png_bytes(on_plate(master, 512, 0.78)),
    }

    # A raster cannot be vectorised, so favicon.svg wraps a PNG. It exists only
    # to keep index.html's type="image/svg+xml" <link> resolving.
    import base64

    b64 = base64.b64encode(png_bytes(rs(256))).decode()
    files[os.path.join(PUBLIC, "favicon.svg")] = (
        '<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"'
        ' viewBox="0 0 256 256" width="256" height="256">'
        f'<image width="256" height="256" xlink:href="data:image/png;base64,{b64}"/>'
        "</svg>"
    ).encode()

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
