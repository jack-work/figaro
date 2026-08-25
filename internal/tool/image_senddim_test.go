package tool

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"testing"
)

// textShot draws a dense high-contrast grid. PNG stores it in almost nothing,
// and DOWNSCALING IT MAKES IT BIGGER, because interpolation invents colours
// where flat runs were. That is the shape that made the fitter hand back
// oversized originals, and it is what a text screenshot looks like to an
// encoder.
func textShot(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	for y := 0; y+8 < h; y += 18 {
		for x := 0; x+5 < w; x += 8 {
			if (x/8+y/18)%5 != 0 {
				draw.Draw(img, image.Rect(x, y, x+5, y+8), &image.Uniform{color.Black}, image.Point{}, draw.Src)
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func dims(t *testing.T, data string) (int, int) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	return cfg.Width, cfg.Height
}

// NOTHING IS EVER SENT OVER MaxSendDim.
//
// The fitter treats MaxDim as a target and will hand back an oversized original
// when shrinking it would cost more bytes than it saves -- a sound trade while
// the only cost is bandwidth, and a fatal one above 2000px, where the provider
// rejects the entire request rather than the image.
//
// Canary: drop the withinDim guard on the passthrough and every case here comes
// back at its original size. The first three are shapes taken from the store.
func TestFitImageNeverExceedsSendDim(t *testing.T) {
	for _, d := range []struct{ w, h int }{
		{2120, 56}, {2800, 640}, {2100, 480},
		{2880, 1800}, // a retina desktop capture
		{1280, 4000}, // a full-page scroll capture: tall, not wide
	} {
		f, err := FitImage(textShot(d.w, d.h), "image/png", DefaultImageLimits())
		if err != nil {
			t.Fatalf("%dx%d: %v", d.w, d.h, err)
		}
		if w, h := dims(t, f.Data); w > DefaultMaxSendDim || h > DefaultMaxSendDim {
			t.Errorf("%dx%d was sent as %dx%d, over the %dpx ceiling", d.w, d.h, w, h, DefaultMaxSendDim)
		}
	}
}

// The passthrough still works where it is safe: an image inside the ceiling
// that would grow on re-encoding is left exactly as it was found.
func TestFitImageStillPassesThroughUnderTheCeiling(t *testing.T) {
	raw := textShot(1900, 700)
	f, err := FitImage(raw, "image/png", DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	if f.Resized || len(f.Data) != base64.StdEncoding.EncodedLen(len(raw)) {
		t.Errorf("1900x700 is under the ceiling and must pass through untouched (resized=%v)", f.Resized)
	}
}
