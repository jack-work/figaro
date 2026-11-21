package render

import (
	"github.com/jack-work/figaro/api/glyph"
	"github.com/jack-work/figaro/api/livedoc"
)

var defaultFrames = []rune(glyph.Frames)

// NodeText is a minimal NodeView: a status header and the body under a gutter.
type NodeText struct {
	Frames []rune // defaultFrames if nil
}

func (r NodeText) Render(n livedoc.Node, width, tick int) []string {
	header := r.glyph(n.Status, tick) + " " + string(n.Type)
	if n.Name != "" {
		header += " " + n.Name
	}
	out := []string{clip(header, width)}
	body := n.Markdown
	if n.Type == livedoc.NodeTool {
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
	case livedoc.StatusOK:
		return glyph.OK
	case livedoc.StatusError:
		return glyph.Fail
	case livedoc.StatusRunning:
		f := r.Frames
		if len(f) == 0 {
			f = defaultFrames
		}
		if tick < 0 {
			tick = -tick
		}
		return string(f[tick%len(f)])
	default:
		return glyph.Pending
	}
}
