package tool

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

// noisyImage builds a w*h image whose pixels do not compress, so its encoded
// size is a real function of its dimensions. A flat-colour test image would
// PNG down to a few hundred bytes at any resolution and would prove nothing
// about a budget measured in bytes.
func noisyImage(w, h int, seed int64) *image.RGBA {
	rng := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: 255,
			})
		}
	}
	return img
}

// screenshotish builds something with the byte-size behaviour of a real screen
// capture: a smooth gradient (which JPEG loves and PNG does not), per-pixel
// dither (which defeats PNG's filters the way anti-aliased text does), and
// high-contrast blocks standing in for glyphs. A flat-colour fixture would PNG
// down to a few hundred bytes at any resolution and would prove nothing about
// a budget measured in bytes.
func screenshotish(w, h int) *image.RGBA {
	rng := rand.New(rand.NewSource(int64(w*7919 + h)))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			// Diagonal gradient.
			base := (x*160/maxInt(w, 1) + y*80/maxInt(h, 1)) + 24
			// Glyph-ish blocks on text rows.
			if (y/14)%3 == 0 && (x/3)%2 == 0 {
				base += 150
			}
			d := rng.Intn(17) - 8 // dither
			img.Set(x, y, color.RGBA{
				R: clamp8(base + d),
				G: clamp8(base + d/2),
				B: clamp8(base + 12 - d),
				A: 255,
			})
		}
	}
	return img
}

func clamp8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func encPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func encJPEG(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}))
	return buf.Bytes()
}

func encGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, img, nil))
	return buf.Bytes()
}

func b64Len(b []byte) int { return base64.StdEncoding.EncodedLen(len(b)) }

// TestFitImagePassesSmallImagesThrough is the invariant that keeps the common
// case free: an image already inside every limit must arrive at the model
// byte-for-byte, not round-tripped through a re-encoder that would cost CPU
// and lose fidelity for nothing.
func TestFitImagePassesSmallImagesThrough(t *testing.T) {
	raw := encPNG(t, noisyImage(64, 48, 1))

	got, err := FitImage(raw, "image/png", DefaultImageLimits())
	require.NoError(t, err)

	assert.Equal(t, base64.StdEncoding.EncodeToString(raw), got.Data,
		"a small image must be forwarded unchanged")
	assert.Equal(t, "image/png", got.MimeType)
	assert.False(t, got.Resized)
	assert.False(t, got.Recoded)
	assert.Equal(t, 1.0, got.Scale())
	assert.Empty(t, got.Note(), "nothing happened, so there is nothing to announce")
}

// TestFitImageScalesToMaxDim pins the resolution ceiling: past Anthropic's own
// downscale threshold, extra pixels are billed and then thrown away by the API.
func TestFitImageScalesToMaxDim(t *testing.T) {
	raw := encPNG(t, screenshotish(3840, 2160))

	got, err := FitImage(raw, "image/png", DefaultImageLimits())
	require.NoError(t, err)

	assert.Equal(t, DefaultMaxImageDim, got.W)
	assert.Equal(t, 882, got.H, "aspect ratio must be preserved")
	assert.Equal(t, 3840, got.OrigW)
	assert.True(t, got.Resized)
	assert.InDelta(t, 2.45, got.Scale(), 0.01)
	assert.Contains(t, got.Note(), "Multiply coordinates by 2.45",
		"a model that clicks what it sees must be able to map back to the real screen")
}

// TestFitImageMeetsByteBudget is the whole point: a picture too big for the
// record is made to fit, not thrown away.
func TestFitImageMeetsByteBudget(t *testing.T) {
	// Built once and shared: encoding is the expensive part of this test, and
	// the fixtures are immutable.
	shot := encPNG(t, screenshotish(2560, 1440))
	noise := encPNG(t, noisyImage(1400, 1000, 7))

	for _, tc := range []struct {
		name   string
		raw    []byte
		budget int
	}{
		{"screenshot into 256KB", shot, 256 << 10},
		{"screenshot into 64KB", shot, 64 << 10},
		{"incompressible noise into 128KB", noise, 128 << 10},
		{"incompressible noise into 16KB", noise, 16 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			require.Greater(t, b64Len(raw), tc.budget, "fixture must actually exceed the budget")

			got, err := FitImage(raw, "image/png", ImageLimits{MaxBase64: tc.budget})
			require.NoError(t, err)

			assert.LessOrEqual(t, len(got.Data), tc.budget,
				"the fitted image must fit the budget it was given")
			assert.Greater(t, len(got.Data), 0)
			assert.True(t, got.W >= 1 && got.H >= 1)

			// And it must still decode as the image type it claims to be.
			decoded, err := base64.StdEncoding.DecodeString(got.Data)
			require.NoError(t, err)
			cfg, format, err := image.DecodeConfig(bytes.NewReader(decoded))
			require.NoError(t, err, "the fitted payload must be a real image")
			assert.Equal(t, got.W, cfg.Width)
			assert.Equal(t, got.H, cfg.Height)
			assert.Equal(t, got.MimeType, "image/"+format)
		})
	}
}

// TestFitImagePrefersLosslessWhenItFits guards the ladder's order. A terminal
// screenshot survives PNG far better than q40 JPEG, so PNG is tried first at
// every rung and only abandoned when it genuinely does not fit.
func TestFitImagePrefersLosslessWhenItFits(t *testing.T) {
	img := screenshotish(600, 400)
	raw := encPNG(t, img)

	// A budget that comfortably holds the PNG at full size.
	got, err := FitImage(raw, "image/png", ImageLimits{MaxBase64: b64Len(raw) * 2})
	require.NoError(t, err)
	assert.Equal(t, "image/png", got.MimeType)

	// A budget below the PNG but above a high-quality JPEG must fall through to
	// JPEG rather than shrinking the picture.
	tight := b64Len(encJPEG(t, img, 85)) + 2048
	require.Less(t, tight, b64Len(raw), "fixture must make PNG the expensive option")
	got, err = FitImage(raw, "image/png", ImageLimits{MaxBase64: tight})
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", got.MimeType)
	assert.True(t, got.Recoded)
	assert.False(t, got.Resized, "quality is spent before pixels are")
	assert.Contains(t, got.Note(), "re-encoded as image/jpeg")
	assert.NotContains(t, got.Note(), "Multiply coordinates",
		"nothing was rescaled, so no coordinate advice must be given")
}

// TestFitImageRecodesUnencodableContainers covers the formats Go can read but
// not write: they pass through untouched when they fit, and become PNG/JPEG
// only when they must be resized.
func TestFitImageRecodesUnencodableContainers(t *testing.T) {
	raw := encGIF(t, screenshotish(64, 64))

	// Fits: keep the container exactly as it was.
	got, err := FitImage(raw, "image/gif", DefaultImageLimits())
	require.NoError(t, err)
	assert.Equal(t, "image/gif", got.MimeType)
	assert.Equal(t, base64.StdEncoding.EncodeToString(raw), got.Data)

	// Does not fit: recode, and say so.
	big := encGIF(t, noisyImage(900, 900, 3))
	got, err = FitImage(big, "image/gif", ImageLimits{MaxBase64: 32 << 10})
	require.NoError(t, err)
	assert.Contains(t, []string{"image/png", "image/jpeg"}, got.MimeType)
	assert.True(t, got.Recoded)
	assert.LessOrEqual(t, len(got.Data), 32<<10)
}

// TestFitImageCorrectsAMislabelledMIME: a JPEG named .png must go on the wire
// as image/jpeg or the API refuses the whole request. The decoder is the
// authority, not the filename.
func TestFitImageCorrectsAMislabelledMIME(t *testing.T) {
	raw := encJPEG(t, screenshotish(200, 200), 90)

	got, err := FitImage(raw, "image/png", DefaultImageLimits())
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", got.MimeType)
}

// TestFitImageFlattensAlphaOverWhite: JPEG has no alpha, and the naive
// conversion paints every transparent pixel BLACK: turning a screenshot with
// a transparent titlebar into a photograph of a void.
func TestFitImageFlattensAlphaOverWhite(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 400))
	// Fully transparent everywhere except a noisy block that defeats PNG, so
	// the fitter is forced down to JPEG.
	noise := noisyImage(400, 200, 11)
	for y := range 200 {
		for x := range 400 {
			img.Set(x, y, noise.At(x, y))
		}
	}
	raw := encPNG(t, img)

	got, err := FitImage(raw, "image/png", ImageLimits{MaxBase64: 16 << 10})
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", got.MimeType)

	decoded, err := base64.StdEncoding.DecodeString(got.Data)
	require.NoError(t, err)
	out, err := jpeg.Decode(bytes.NewReader(decoded))
	require.NoError(t, err)

	// The transparent half must have become white, not black.
	b := out.Bounds()
	r, g, bl, _ := out.At(b.Dx()/2, b.Dy()*3/4).RGBA()
	assert.Greater(t, int(r>>8), 200, "transparent pixels must flatten to white")
	assert.Greater(t, int(g>>8), 200)
	assert.Greater(t, int(bl>>8), 200)
}

// TestFitImageRefusesTheImpossible covers the two honest failures: a budget
// too small for any legible picture, and a decode bomb.
func TestFitImageRefusesTheImpossible(t *testing.T) {
	raw := encPNG(t, noisyImage(400, 400, 5))

	_, err := FitImage(raw, "image/png", ImageLimits{MaxBase64: 512})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no legible image fits")

	_, err = FitImage(raw, "image/png", ImageLimits{MaxPixels: 16})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode limit")

	_, err = FitImage([]byte("not an image at all"), "image/png", DefaultImageLimits())
	require.Error(t, err)
}

// TestReadToolFitsLargeImages is the end of the tool path: read must hand the
// turn loop an image that already fits, with a note that explains what it did.
func TestReadToolFitsLargeImages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "screenshot.png")
	raw := encPNG(t, screenshotish(3200, 1800))
	require.NoError(t, os.WriteFile(path, raw, 0o644))

	const budget = 200 << 10
	rt := &ReadTool{CwdFn: staticCwd(dir), ImageLimits: ImageLimits{MaxBase64: budget}}

	var streamed string
	content, err := rt.Execute(t.Context(), map[string]any{"path": "screenshot.png"},
		func(b []byte) { streamed += string(b) })
	require.NoError(t, err)
	require.Len(t, content, 2, "a note and the picture")

	assert.Equal(t, message.ContentProse, content[0].Type)
	assert.Contains(t, content[0].Text, "[Image: screenshot.png")
	assert.Contains(t, content[0].Text, "Multiply coordinates by")
	assert.Equal(t, content[0].Text, streamed, "the UI sees what the model sees")

	assert.Equal(t, message.ContentImage, content[1].Type)
	assert.LessOrEqual(t, len(content[1].Data), budget)
	assert.NotEmpty(t, content[1].MimeType)
}

// TestReadToolSmallImageIsUnchanged pins the no-op path: an ordinary small
// image must reach the model exactly as it sits on disk, with the plain note
// it always had.
func TestReadToolSmallImageIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "icon.png")
	raw := encPNG(t, screenshotish(32, 32))
	require.NoError(t, os.WriteFile(path, raw, 0o644))

	rt := &ReadTool{CwdFn: staticCwd(dir)}
	content, err := rt.Execute(t.Context(), map[string]any{"path": "icon.png"}, nil)
	require.NoError(t, err)
	require.Len(t, content, 2)

	assert.Equal(t, base64.StdEncoding.EncodeToString(raw), content[1].Data)
	assert.Equal(t, "image/png", content[1].MimeType)
	assert.NotContains(t, content[0].Text, "Multiply coordinates")
	assert.NotContains(t, content[0].Text, "omitted")
}

// TestReadToolAnnouncesAnUnfittableImage: when even the fitter gives up, the
// read still succeeds and the model is TOLD it is blind. Silence here is the
// original bug.
func TestReadToolAnnouncesAnUnfittableImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	require.NoError(t, os.WriteFile(path, encPNG(t, noisyImage(800, 800, 9)), 0o644))

	rt := &ReadTool{CwdFn: staticCwd(dir), ImageLimits: ImageLimits{MaxBase64: 1024}}
	content, err := rt.Execute(t.Context(), map[string]any{"path": "huge.png"}, nil)
	require.NoError(t, err, "an unfittable image is not a failed read")
	require.Len(t, content, 1, "text only: there is no picture to send")

	assert.Contains(t, content[0].Text, "image omitted")
	assert.Contains(t, content[0].Text, "cropped or smaller copy",
		"tell the model what it can do about it")
}

// TestReadToolTextFilesAreUntouched guards the blast radius: nothing about
// this change may alter how an ordinary file is read.
func TestReadToolTextFilesAreUntouched(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644))

	rt := &ReadTool{CwdFn: staticCwd(dir)}
	content, err := rt.Execute(t.Context(), map[string]any{"path": "a.txt"}, nil)
	require.NoError(t, err)
	require.Len(t, content, 1)
	assert.Equal(t, message.ContentProse, content[0].Type)
	assert.Contains(t, content[0].Text, "hello")
}
