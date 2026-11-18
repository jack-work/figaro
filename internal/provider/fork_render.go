package provider

import (
	"encoding/json"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// ForkReminderTexts renders the fork incantation on the message carrying a
// branch's birth mark.
func ForkReminderTexts(msg message.Message, bound form.Snapshot) []string {
	parent, forked := forkedFrom(msg.Patches)
	if !forked {
		return nil
	}
	say := form.ReadForkIncantation(bound).OnFork
	if say == "" {
		return nil
	}
	return []string{Reminder("fork", "from "+parent+"\n"+say)}
}

func forkedFrom(patches []message.Patch) (string, bool) {
	for _, p := range patches {
		e, ok := form.RuntimeEntry(p, form.ForkedFromKey)
		if !ok || e.IsRemoval() {
			continue
		}
		var parent string
		if err := json.Unmarshal(e.New, &parent); err != nil {
			continue
		}
		if parent = strings.TrimSpace(parent); parent != "" {
			return parent, true
		}
	}
	return "", false
}
