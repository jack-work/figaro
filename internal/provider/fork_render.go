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
	parent, forked := forkedFrom(msg.Patches, bound)
	if !forked {
		return nil
	}
	say := form.ReadForkIncantation(bound).OnFork
	if say == "" {
		return nil
	}
	return []string{Reminder("fork", "from "+parent+"\n"+say)}
}

func forkedFrom(patches []message.Patch, before form.Snapshot) (string, bool) {
	for _, p := range patches {
		if parent := stampedParent(p); parent != "" && parent != heldParent(before) {
			return parent, true
		}
		before = before.Apply(p)
	}
	return "", false
}

func stampedParent(p message.Patch) string {
	e, ok := form.RuntimeEntry(p, form.ForkedFromKey)
	if !ok || e.IsRemoval() {
		return ""
	}
	return parentID(e.New)
}

func heldParent(s form.Snapshot) string {
	raw, ok := form.RuntimeGet(s, form.ForkedFromKey)
	if !ok {
		return ""
	}
	return parentID(raw)
}

func parentID(raw json.RawMessage) string {
	var parent string
	if err := json.Unmarshal(raw, &parent); err != nil {
		return ""
	}
	return strings.TrimSpace(parent)
}
