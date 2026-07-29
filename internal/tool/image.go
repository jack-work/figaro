// Image fitting for tool-produced imagery.
//
// A tool that returns a picture (today: read, on an image file) hands the turn
// loop base64 that must survive two independent ceilings:
//
//  1. the PROVIDER's inline-image limit, and the resolution past which a
//     provider downscales server-side anyway — bytes above it buy nothing; and
//  2. the STORE's: the tool_result tic is ONE figwal record, and a record that
//     does not fit inside a WAL segment fails the append and takes the turn
//     with it.
//
// Dropping the image satisfies both and helps nobody — it reproduces, at a
// different threshold, exactly the blindness this path exists to end. Fitting
// it satisfies both and keeps the picture. So the ceiling is a target to
// encode toward, not a wall to fall off: scale down, try lossless first, and
// only refuse an image that cannot be made to fit at any size.
package tool

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"

	_ "image/gif" // decode animated/static GIF (first frame)

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decode-only; re-encoded as PNG/JPEG
)

// ImageLimits bounds one inlined image.
type ImageLimits struct {
	// MaxDim is the longest edge in pixels. Anthropic scales anything above
	// 1568px down on its own, so pixels past it cost bytes and buy nothing.
	MaxDim int
	// MaxBase64 caps the encoded payload — the number that actually decides
	// whether the WAL record fits.
	MaxBase64 int
	// MaxPixels refuses a decode bomb before it is allocated: a small file can
	// declare an enormous canvas, and decoding it would cost 4 bytes of RAM per
	// pixel inside the tool call.
	MaxPixels int
}

const (
	// DefaultMaxImageDim is Anthropic's own downscale threshold. Above it the
	// API resizes server-side, so sending more is pure waste.
	DefaultMaxImageDim = 1568
	// DefaultMaxImagePixels caps decode cost at ~64Mpx (~256MB as RGBA).
	DefaultMaxImagePixels = 64 << 20
	// ProviderImageCeiling is the smallest inline-image limit across the
	// providers figaro speaks to (Anthropic: 5MB per image), with headroom for
	// the JSON envelope. The store's segment budget is usually stricter; the
	// caller passes whichever binds.
	ProviderImageCeiling = 3500 << 10
	// minUsefulImageBytes is the floor below which a picture has been scaled
	// into uselessness and is better refused than sent as mud.
	minUsefulImageBytes = 8 << 10
)

// DefaultImageLimits is the fallback for a caller with no configuration —
// tests, one-off registries, `figaro read`.
func DefaultImageLimits() ImageLimits {
	return ImageLimits{
		MaxDim:    DefaultMaxImageDim,
		MaxBase64: ProviderImageCeiling,
		MaxPixels: DefaultMaxImagePixels,
	}
}

// withDefaults fills the zero fields so a partially-specified limit set (the
// common case: "same as default, but this many bytes") behaves.
func (l ImageLimits) withDefaults() ImageLimits {
	d := DefaultImageLimits()
	if l.MaxDim <= 0 {
		l.MaxDim = d.MaxDim
	}
	if l.MaxBase64 <= 0 {
		l.MaxBase64 = d.MaxBase64
	}
	if l.MaxPixels <= 0 {
		l.MaxPixels = d.MaxPixels
	}
	return l
}

// FittedImage is one image made to fit, plus everything a caller needs to tell
// the model what happened to it.
type FittedImage struct {
	Data     string // base64 of the encoded image
	MimeType string
	OrigW    int
	OrigH    int
	W        int
	H        int
	// Resized reports that pixels were dropped, so a coordinate read off this
	// image does not address the original.
	Resized bool
	// Recoded reports a container change (webp -> png, png -> jpeg).
	Recoded bool
}

// Scale is the factor mapping a coordinate on the fitted image back onto the
// original. 1 when nothing was resized.
func (f FittedImage) Scale() float64 {
	if f.W == 0 || !f.Resized {
		return 1
	}
	return float64(f.OrigW) / float64(f.W)
}

// Note is the line handed to the model alongside the picture. It exists
// because a model that clicks where it sees a button must be able to convert
// that coordinate back to the real screen; without the factor, every
// resize silently corrupts every coordinate the model derives.
func (f FittedImage) Note() string {
	switch {
	case f.Resized && f.Recoded:
		return fmt.Sprintf(
			"[Image: original %dx%d, sent as %s at %dx%d. Multiply coordinates by %.2f to map to the original.]",
			f.OrigW, f.OrigH, f.MimeType, f.W, f.H, f.Scale())
	case f.Resized:
		return fmt.Sprintf(
			"[Image: original %dx%d, sent at %dx%d. Multiply coordinates by %.2f to map to the original.]",
			f.OrigW, f.OrigH, f.W, f.H, f.Scale())
	case f.Recoded:
		return fmt.Sprintf("[Image: %dx%d, re-encoded as %s.]", f.W, f.H, f.MimeType)
	}
	return ""
}

// ErrImageUnfittable reports an image that could not be encoded under the
// ceiling at any size worth sending.
type ErrImageUnfittable struct {
	W, H  int
	Limit int
}

func (e ErrImageUnfittable) Error() string {
	if e.W == 0 && e.H == 0 {
		return fmt.Sprintf("no legible image fits under %s", FormatSize(e.Limit))
	}
	return fmt.Sprintf("image %dx%d cannot be encoded under %s", e.W, e.H, FormatSize(e.Limit))
}

// FitImage makes raw fit inside lim, preferring the least destructive step
// that works:
//
//	pass through -> scale to MaxDim -> PNG -> JPEG down a quality ladder ->
//	scale down 25% and try again -> ... -> refuse
//
// PNG is tried before JPEG at every rung so a screenshot of text stays
// lossless whenever lossless is affordable.
func FitImage(raw []byte, mime string, lim ImageLimits) (FittedImage, error) {
	lim = lim.withDefaults()
	// A budget too small to hold a legible picture cannot be met by shrinking;
	// refuse it up front rather than grinding down to a 1x1 smudge.
	if lim.MaxBase64 < minUsefulImageBytes {
		return FittedImage{}, ErrImageUnfittable{Limit: lim.MaxBase64}
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return FittedImage{}, fmt.Errorf("decode image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return FittedImage{}, fmt.Errorf("image has no extent (%dx%d)", cfg.Width, cfg.Height)
	}
	if px := int64(cfg.Width) * int64(cfg.Height); px > int64(lim.MaxPixels) {
		return FittedImage{}, fmt.Errorf(
			"image %dx%d exceeds the %d-pixel decode limit", cfg.Width, cfg.Height, lim.MaxPixels)
	}

	// Cheapest possible outcome: the bytes on disk are already acceptable and
	// the container is one every provider takes. Re-encoding here would only
	// lose fidelity and burn CPU.
	fits := inlineableFormat(format) && base64.StdEncoding.EncodedLen(len(raw)) <= lim.MaxBase64
	if fits && cfg.Width <= lim.MaxDim && cfg.Height <= lim.MaxDim {
		return passthrough(raw, cfg, format, mime), nil
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return FittedImage{}, fmt.Errorf("decode image: %w", err)
	}
	origW, origH := src.Bounds().Dx(), src.Bounds().Dy()
	lossy := format == "jpeg"

	w, h := fitWithin(origW, origH, lim.MaxDim)
	for {
		scaled := scaleTo(src, w, h)
		if data, mt, ok := encodeUnder(scaled, lim.MaxBase64, lossy); ok {
			fitted := FittedImage{
				Data:     data,
				MimeType: mt,
				OrigW:    origW, OrigH: origH,
				W: w, H: h,
				Resized: w != origW || h != origH,
				Recoded: mt != canonicalMIME(format, mime),
			}
			// Resizing to MaxDim is an OPTIMIZATION, not a requirement — the
			// providers accept larger images, they merely downscale them. So when
			// the original already fit the byte budget, only take the re-encode if
			// it actually saved bytes. Re-encoding a 2% oversized PNG, or a photo
			// that lands larger as PNG than it was as JPEG, is pure loss.
			if fits && len(fitted.Data) >= base64.StdEncoding.EncodedLen(len(raw)) {
				return passthrough(raw, cfg, format, mime), nil
			}
			return fitted, nil
		}
		nw, nh := shrink(w, h)
		if nw == w && nh == h {
			break // 1x1 and still over budget: nothing left to try
		}
		w, h = nw, nh
	}
	return FittedImage{}, ErrImageUnfittable{W: origW, H: origH, Limit: lim.MaxBase64}
}

// passthrough forwards the bytes exactly as they sit on disk.
func passthrough(raw []byte, cfg image.Config, format, mime string) FittedImage {
	return FittedImage{
		Data:     base64.StdEncoding.EncodeToString(raw),
		MimeType: canonicalMIME(format, mime),
		OrigW:    cfg.Width, OrigH: cfg.Height,
		W: cfg.Width, H: cfg.Height,
	}
}

// encodeUnder returns the first encoding of img that lands under limit.
//
// The order follows the SOURCE. A screenshot arrives lossless and survives PNG
// far better than it survives a quality ladder, so PNG is tried first and
// abandoned only when it genuinely does not fit. A photograph arrives as JPEG
// and has already paid for its artifacts — re-encoding it as PNG would faithfully
// preserve those artifacts at several times the size, which is the worst of both.
func encodeUnder(img image.Image, limit int, lossy bool) (data, mime string, ok bool) {
	tryPNG := func() (string, string, bool) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return "", "", false
		}
		if base64.StdEncoding.EncodedLen(buf.Len()) > limit {
			return "", "", false
		}
		return base64.StdEncoding.EncodeToString(buf.Bytes()), "image/png", true
	}
	tryJPEG := func() (string, string, bool) {
		flat := flattenAlpha(img)
		var buf bytes.Buffer
		for _, q := range []int{85, 70, 55, 40} {
			buf.Reset()
			if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: q}); err != nil {
				continue
			}
			if base64.StdEncoding.EncodedLen(buf.Len()) <= limit {
				return base64.StdEncoding.EncodeToString(buf.Bytes()), "image/jpeg", true
			}
		}
		return "", "", false
	}

	first, second := tryPNG, tryJPEG
	if lossy {
		first, second = tryJPEG, tryPNG
	}
	if d, m, ok := first(); ok {
		return d, m, true
	}
	return second()
}

// flattenAlpha composites over white. JPEG has no alpha channel, and the
// naive conversion renders every transparent pixel BLACK — which turns a
// screenshot with a transparent titlebar into a photograph of a void.
func flattenAlpha(img image.Image) image.Image {
	if op, ok := img.(interface{ Opaque() bool }); ok && op.Opaque() {
		return img
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	draw.Draw(out, b, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(out, b, img, b.Min, draw.Over)
	return out
}

// scaleTo resamples with Catmull-Rom, which keeps text edges readable at the
// downscales a screenshot actually undergoes. Returns src untouched when the
// target is the current size, so the pass-through path costs nothing.
func scaleTo(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// fitWithin scales (w,h) down to fit a square of max, preserving aspect.
func fitWithin(w, h, max int) (int, int) {
	if w <= max && h <= max {
		return w, h
	}
	if w >= h {
		return max, maxInt(1, int(float64(h)*float64(max)/float64(w)+0.5))
	}
	return maxInt(1, int(float64(w)*float64(max)/float64(h)+0.5)), max
}

// shrink takes one 25% step down, never below 1x1.
func shrink(w, h int) (int, int) {
	nw, nh := maxInt(1, w*3/4), maxInt(1, h*3/4)
	if nw == w && nh == h {
		return w, h
	}
	return nw, nh
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// inlineableFormat reports whether a decoded format can be sent to the model
// byte-for-byte. Every provider figaro speaks to accepts all four, so an image
// already under the ceiling is never re-encoded — WebP keeps its container and
// an animated GIF keeps its frames. Only the RESIZE path has to recode, since
// pure Go can encode neither WebP nor multi-frame GIF.
func inlineableFormat(format string) bool {
	switch format {
	case "png", "jpeg", "gif", "webp":
		return true
	}
	return false
}

// canonicalMIME prefers the format the decoder actually recognised over the
// caller's guess: a ".png" that is really a JPEG must be labelled JPEG or the
// API rejects it.
func canonicalMIME(format, fallback string) string {
	switch format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	}
	return fallback
}
