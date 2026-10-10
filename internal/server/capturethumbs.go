package server

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"

	_ "image/gif" // register a decoder a backend may answer with

	"github.com/tidwall/gjson"
)

// An image route's response body is dropped from its capture (see
// captureFieldsByPath): a rendered PNG is megabytes of base64, which zstd barely
// shrinks, and the default capture buffer is 5 MB, so two renders would push
// every chat capture out of it. What the Activity viewer needs is to SEE the
// result, so these routes store a downscaled copy of each image instead, a few
// tens of KB apiece.

// captureThumb is one downscaled result image stored with a capture.
type captureThumb struct {
	Mime string `json:"mime"`
	Data []byte `json:"data"`
}

const (
	thumbMaxEdge   = 384
	thumbMaxImages = 4
)

// responseThumbs pulls the base64 images out of an image-route JSON body
// (A1111 `images[]`, OpenAI `data[].b64_json`) and returns a thumbnail of each.
// Anything that does not decode is skipped; this is a viewer convenience and
// never fails the capture.
func responseThumbs(body []byte) []captureThumb {
	if !gjson.ValidBytes(body) {
		return nil
	}
	parsed := gjson.ParseBytes(body)
	var b64s []string
	parsed.Get("images").ForEach(func(_, v gjson.Result) bool {
		b64s = append(b64s, v.String())
		return len(b64s) < thumbMaxImages
	})
	parsed.Get("data").ForEach(func(_, v gjson.Result) bool {
		if s := v.Get("b64_json").String(); s != "" {
			b64s = append(b64s, s)
		}
		return len(b64s) < thumbMaxImages
	})

	var out []captureThumb
	for _, s := range b64s {
		if t, ok := makeThumb(s); ok {
			out = append(out, t)
		}
	}
	return out
}

// makeThumb decodes one base64 image (bare or data: URL) and re-encodes it at
// most thumbMaxEdge on its long side. An image with any transparency stays PNG
// so a cut-out still reads as one; an opaque one goes to JPEG, about a tenth of
// the size.
func makeThumb(s string) (captureThumb, bool) {
	if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i >= 0 {
		s = s[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return captureThumb{}, false
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return captureThumb{}, false
	}
	dst := downscale(src, thumbMaxEdge)

	var buf bytes.Buffer
	if hasAlpha(dst) {
		if err := png.Encode(&buf, dst); err != nil {
			return captureThumb{}, false
		}
		return captureThumb{Mime: "image/png", Data: buf.Bytes()}, true
	}
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return captureThumb{}, false
	}
	return captureThumb{Mime: "image/jpeg", Data: buf.Bytes()}, true
}

// downscale box-filters src so its long edge is at most maxEdge. Each output
// pixel averages the source rectangle it covers (premultiplied, so transparent
// pixels do not bleed their colour into the edge of a cut-out). Small images
// are copied unscaled.
func downscale(src image.Image, maxEdge int) *image.NRGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := sw, sh
	if sw >= sh && sw > maxEdge {
		dw, dh = maxEdge, max(1, sh*maxEdge/sw)
	} else if sh > sw && sh > maxEdge {
		dw, dh = max(1, sw*maxEdge/sh), maxEdge
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca)
					n++
				}
			}
			c := color.NRGBA{A: uint8(a / n >> 8)}
			if a > 0 {
				c.R = uint8(r * 0xffff / a >> 8)
				c.G = uint8(g * 0xffff / a >> 8)
				c.B = uint8(bl * 0xffff / a >> 8)
			}
			dst.SetNRGBA(x, y, c)
		}
	}
	return dst
}

func hasAlpha(img *image.NRGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0xff {
			return true
		}
	}
	return false
}
