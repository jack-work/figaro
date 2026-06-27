package render

import "github.com/jack-work/figaro/internal/livelog/doc"

// BlockRenderer turns a block into terminal lines. Each returned line must be a
// single physical line no wider than width (use clip). The tick advances
// animations (e.g. spinners). Implementations interpret doc.Block.Kind/Attrs;
// tests inject a trivial one to isolate the differential renderer from content.
type BlockRenderer interface {
	Render(b doc.Block, width, tick int) []string
}

// defaultFrames is the braille spinner used for active blocks.
var defaultFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// TextRenderer is a minimal, generic BlockRenderer: a header line (status glyph
// + kind + optional Attrs["title"]) followed by the body wrapped under a gutter.
// It is enough to exercise the full pipeline and a reasonable base for richer
// renderers.
type TextRenderer struct {
	Frames []rune // spinner frames for StatusActive; defaultFrames if nil
}

func (r TextRenderer) Render(b doc.Block, width, tick int) []string {
	header := r.glyph(b.Status, tick) + " " + b.Kind
	if title := b.Attrs["title"]; title != "" {
		header += " " + title
	}
	out := []string{clip(header, width)}
	if b.Body != "" {
		for _, l := range hardWrap(b.Body, width-2) {
			out = append(out, clip("  "+l, width))
		}
	}
	return out
}

func (r TextRenderer) glyph(s doc.Status, tick int) string {
	switch s {
	case doc.StatusOK:
		return "✓"
	case doc.StatusError:
		return "✗"
	case doc.StatusActive:
		f := r.Frames
		if len(f) == 0 {
			f = defaultFrames
		}
		if tick < 0 {
			tick = -tick
		}
		return string(f[tick%len(f)])
	default:
		// No explicit status (prose, thinking, …): a static marker, never an
		// animated spinner — only StatusActive animates.
		return "·"
	}
}
