package cli

import (
	"strconv"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/config"
)

// Shared with the search prefilter (messageMayRenderQuery): both must agree on a
// coordinate row's text.
const coordSep = " · "

// A tool's At is unset until it is folded; FinishedAt would change the row's shape
// when the spinner stops.
func nodeCoordAt(n livedoc.Node) int64 {
	if n.At != 0 {
		return n.At
	}
	return n.StartedAt
}

// A zero timestamp prints no time rather than 1970.
func coordLabel(turn, node int, at int64, layout string) string {
	s := strconv.Itoa(turn)
	if node >= 0 {
		s += "." + strconv.Itoa(node)
	}
	if at != 0 {
		if layout == "" {
			layout = config.CoordFormatDefault
		}
		s += coordSep + time.UnixMilli(at).Format(layout)
	}
	return s
}

func (t *transcript) verbose() bool {
	view, ok := t.view.(*ariaView)
	return ok && view.settings != nil && view.settings.verbose
}

func (t *transcript) coordFormat() string {
	if view, ok := t.view.(*ariaView); ok && view.settings != nil {
		return view.settings.coordFormat
	}
	return config.CoordFormatDefault
}
