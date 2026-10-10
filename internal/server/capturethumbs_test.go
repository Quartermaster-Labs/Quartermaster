package server

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encodePNGB64(t *testing.T, w, h int, a uint8) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 40, 40, a
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestResponseThumbs_SDAPIAndOpenAI(t *testing.T) {
	opaque := encodePNGB64(t, 1024, 768, 0xff)
	sd := []byte(`{"images":["` + opaque + `"],"info":""}`)
	thumbs := responseThumbs(sd)
	if len(thumbs) != 1 || thumbs[0].Mime != "image/jpeg" {
		t.Fatalf("sdapi: got %+v", thumbs)
	}
	img, _, err := image.Decode(bytes.NewReader(thumbs[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != thumbMaxEdge || b.Dy() != 288 {
		t.Fatalf("thumb size = %v, want 384x288", b)
	}

	clear := encodePNGB64(t, 64, 64, 0)
	oa := []byte(`{"data":[{"b64_json":"` + clear + `"}]}`)
	thumbs = responseThumbs(oa)
	if len(thumbs) != 1 || thumbs[0].Mime != "image/png" {
		t.Fatalf("openai transparent: got %+v", thumbs)
	}
}

func TestResponseThumbs_SkipsGarbage(t *testing.T) {
	if got := responseThumbs([]byte(`{"images":["not-base64!"]}`)); len(got) != 0 {
		t.Fatalf("want none, got %d", len(got))
	}
	if got := responseThumbs([]byte(`not json`)); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestDownscale_PremultipliedEdge(t *testing.T) {
	// A half-transparent-black / half-opaque-red 2x1 averaged to 1x1 must stay
	// red, not darken toward the invisible pixel's colour.
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 0})
	src.SetNRGBA(1, 0, color.NRGBA{255, 0, 0, 255})
	got := downscale(src, 1).NRGBAAt(0, 0)
	if got.R < 250 || got.A < 120 || got.A > 135 {
		t.Fatalf("got %+v", got)
	}
}
