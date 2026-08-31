#!/usr/bin/env python3
"""The quartermaster mark, as geometry.

The mark is a ship's wheel: a bright rim over a darker cross, eight handles,
a bright hub. It exists in two forms, and which one a surface gets is a
property of the surface, not a style choice:

  naked   transparent, no ground. For surfaces that already have one and are
          small: the in-app title bar and the browser tab.
  plated  on a dark rounded box with a slight diagonal gradient. For surfaces
          that sit on somebody else's background -- taskbar, tray, desktop,
          installer -- where a transparent mark has nothing to sit on and a
          flat black one disappears into a dark taskbar.

Geometry is declared once, as a list of parts, and consumed twice: by Pillow to
rasterise and by a small emitter to write SVG. Both read the same list, so the
.svg and the .png cannot drift apart.

Two rules hold the drawing together, and both were arrived at the hard way:

  * A part is one flat colour. Shading is never a shape laid over the wheel --
    that reads as a decal, because its edges do not line up with anything.
  * Where two parts of different colours meet, the seam is hidden under a third
    part rather than made to agree. The cross therefore stops at the rim's
    CENTRELINE: far enough under the rim that its butt end can never show a
    hairline at 16px, not so far that it emerges on the far side and fuses with
    the handles, which have to stay separated from it by the rim.
"""
import math

S = 256                       # the design grid; every number below is on it

O_BRAND = "#FF6A2B"           # the site accent: rim, hub
O_DEEP = "#B8410E"            # cross, handles
PLATE_FROM, PLATE_TO = "#262A31", "#141519"   # plate gradient, top-left to bottom-right

C = 128.0                     # centre
R_OUT, R_IN = 104.0, 78.0     # rim, outer and inner edge
SPOKE_W = 18.0
HUB_R = 25.0
H_LEN, H_W = 15.0, 18.0       # handles, beyond the rim
SPOKES = (45, 135, 225, 315)
HANDLES = (0, 45, 90, 135, 180, 225, 270, 315)

PLATE_RADIUS = 0.22           # corner radius, as a fraction of the side
PLATE_INSET = 0.76            # how much of the plate the wheel fills


def _pol(a, r):
    t = math.radians(a)
    return C + r * math.cos(t), C + r * math.sin(t)


def _parts():
    """The wheel, in paint order: cross, rim, handles, hub."""
    ops = [("bar", a, 0.0, (R_IN + R_OUT) / 2, SPOKE_W, O_DEEP) for a in SPOKES]
    ops.append(("rim", (R_OUT + R_IN) / 2, R_OUT - R_IN, O_BRAND))
    ops += [("stub", a, R_OUT, R_OUT + H_LEN, H_W, O_DEEP) for a in HANDLES]
    ops.append(("hub", HUB_R, O_BRAND))
    return ops


PARTS = _parts()


# ------------------------------------------------------------------ raster
def render(size, supersample=4):
    """The naked mark, as an RGBA image on transparency."""
    from PIL import Image, ImageDraw

    k = supersample
    img = Image.new("RGBA", (S * k, S * k), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    for op in PARTS:
        kind = op[0]
        if kind == "rim":
            _, r, w, col = op
            d.arc([(C - r) * k, (C - r) * k, (C + r) * k, (C + r) * k],
                  0, 360, fill=col, width=int(w * k))
        elif kind in ("bar", "stub"):
            _, a, r0, r1, w, col = op
            x0, y0 = _pol(a, r0)
            x1, y1 = _pol(a, r1)
            d.line([x0 * k, y0 * k, x1 * k, y1 * k], fill=col, width=int(w * k))
            if kind == "stub":            # round the free end only; butt ends hide
                d.ellipse([(x1 - w / 2) * k, (y1 - w / 2) * k,
                           (x1 + w / 2) * k, (y1 + w / 2) * k], fill=col)
        elif kind == "hub":
            _, r, col = op
            d.ellipse([(C - r) * k, (C - r) * k, (C + r) * k, (C + r) * k], fill=col)
    return img.resize((size, size), Image.LANCZOS)


def _rgb(h):
    return tuple(int(h[i:i + 2], 16) for i in (1, 3, 5))


def render_plated(size, supersample=4):
    """The mark on its dark rounded plate."""
    from PIL import Image, ImageDraw

    a, b = _rgb(PLATE_FROM), _rgb(PLATE_TO)
    grad = Image.new("RGB", (size, size))
    px = grad.load()
    span = 2 * (size - 1) or 1
    for y in range(size):
        for x in range(size):
            t = (x + y) / span
            px[x, y] = tuple(int(a[i] + (b[i] - a[i]) * t) for i in range(3))

    k = supersample                       # the corners need antialiasing
    mask = Image.new("L", (size * k, size * k), 0)
    ImageDraw.Draw(mask).rounded_rectangle(
        [0, 0, size * k - 1, size * k - 1], radius=int(size * k * PLATE_RADIUS), fill=255)

    out = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    out.paste(grad, (0, 0), mask.resize((size, size), Image.LANCZOS))
    inner = int(size * PLATE_INSET)
    art = render(inner, supersample)
    out.paste(art, ((size - inner) // 2, (size - inner) // 2), art)
    return out


# --------------------------------------------------------------------- svg
def _paths():
    out = []
    for op in PARTS:
        kind = op[0]
        if kind == "rim":
            _, r, w, col = op
            out.append(f'<circle cx="{C:g}" cy="{C:g}" r="{r:g}" fill="none" '
                       f'stroke="{col}" stroke-width="{w:g}"/>')
        elif kind in ("bar", "stub"):
            _, a, r0, r1, w, col = op
            x0, y0 = _pol(a, r0)
            x1, y1 = _pol(a, r1)
            cap = "round" if kind == "stub" else "butt"
            out.append(f'<path d="M{x0:.2f} {y0:.2f} L{x1:.2f} {y1:.2f}" stroke="{col}" '
                       f'stroke-width="{w:g}" stroke-linecap="{cap}"/>')
        elif kind == "hub":
            _, r, col = op
            out.append(f'<circle cx="{C:g}" cy="{C:g}" r="{r:g}" fill="{col}"/>')
    return out


def svg():
    """The naked mark."""
    body = "\n".join(_paths())
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {S} {S}" '
            f'width="{S}" height="{S}" fill="none">\n{body}\n</svg>\n')


def svg_plated():
    """The mark on its plate, the wheel scaled into the plate's safe area."""
    body = "\n  ".join(_paths())
    off = S * (1 - PLATE_INSET) / 2
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {S} {S}" '
            f'width="{S}" height="{S}" fill="none">\n'
            f'<defs><linearGradient id="plate" x1="0%" y1="0%" x2="100%" y2="100%">\n'
            f'<stop offset="0" stop-color="{PLATE_FROM}"/>'
            f'<stop offset="1" stop-color="{PLATE_TO}"/>\n'
            f'</linearGradient></defs>\n'
            f'<rect width="{S}" height="{S}" rx="{S * PLATE_RADIUS:g}" fill="url(#plate)"/>\n'
            f'<g transform="translate({off:g} {off:g}) scale({PLATE_INSET:g})">\n'
            f'  {body}\n</g>\n</svg>\n')


def svg_inline(size, attrs='', colors=None):
    """One line, for pasting into a page's markup: the site header's brand mark.

    `colors` re-maps the two tones to arbitrary CSS, which is how the site keeps
    the mark on its own accent: it is drawn in currentColor plus one variable,
    so the light theme's deliberately darker orange applies to the mark too
    instead of the app's bright one washing out on cream.
    """
    body = "".join(_paths())
    for old, new in (colors or {}).items():
        body = body.replace(f'"{old}"', f'"{new}"')
    a = (" " + attrs.strip()) if attrs else ""
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {S} {S}" '
            f'width="{size}" height="{size}" fill="none"{a}>{body}</svg>')
