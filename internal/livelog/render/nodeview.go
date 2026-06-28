package render

import "github.com/jack-work/figaro/internal/livelog/aria"

// NodeText is a minimal NodeView: a header (status glyph + type + optional name)
// and the streamed body under a gutter. Tools show output; prose/thinking show
// markdown. Only a running tool animates; everything else gets a static marker.
type NodeText struct {
	Frames []rune // spinner frames; defaultFrames if nil
}

func (r NodeText) Render(n aria.Node, width, tick int) []string {
	header := r.glyph(n.Status, tick) + " " + n.Type
	if n.Name != "" {
		header += " " + n.Name
	}
	out := []string{clip(header, width)}
	body := n.Markdown
	if n.Type == "tool" {
		body = n.Output
	}
	if body != "" {
		for _, l := range hardWrap(body, width-2) {
			out = append(out, clip("  "+l, width))
		}
	}
	return out
}

func (r NodeText) glyph(status string, tick int) string {
	switch status {
	case "ok":
		return "✓"
	case "error":
		return "✗"
	case "running":
		f := r.Frames
		if len(f) == 0 {
			f = defaultFrames
		}
		if tick < 0 {
			tick = -tick
		}
		return string(f[tick%len(f)])
	default:
		return "·"
	}
}
