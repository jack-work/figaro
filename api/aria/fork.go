package aria

import (
	"encoding/json"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
)

// ForkedFromOf is the parent a turn's form deltas record as the bound form's
// birth stamp, or "" when they record none.
func ForkedFromOf(deltas map[string]livedoc.FormDelta) string {
	legacy, _ := form.LegacyName(form.ForkedFromKey)
	for key, d := range deltas {
		if d.Kind != livedoc.FormBound || d.Event != livedoc.FormSet {
			continue
		}
		bare := strings.TrimPrefix(key, d.Form+".")
		if bare != form.ForkedFromKey && bare != legacy {
			continue
		}
		var parent string
		if json.Unmarshal(d.Value, &parent) == nil && parent != "" {
			return parent
		}
		return strings.TrimSpace(strings.Trim(string(d.Value), `"`))
	}
	return ""
}
