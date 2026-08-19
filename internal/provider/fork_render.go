package provider

// The FORK incantation: what a branch is told about being a branch.

import (
	"encoding/json"
	"strings"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

// ForkReminderTexts renders the fork incantation when this message carries the
// patch that marks a branch's birth. One block, or none.
func ForkReminderTexts(msg message.Message, board form.Snapshot) []string {
	parent, forked := forkedFrom(msg.Patches)
	if !forked {
		return nil
	}
	say := form.ReadForkIncantation(board).OnFork
	if say == "" {
		return nil
	}
	body := map[string]any{"forked_from": parent, "say": say}
	b, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return []string{"<system-reminder name=\"fork\">\n" + string(b) + "\n</system-reminder>"}
}

// forkedFrom finds the birth mark in a message's board transitions.
func forkedFrom(patches []message.Patch) (string, bool) {
	for _, p := range patches {
		raw, ok := p.Set[form.ForkedFromKey]
		if !ok {
			continue
		}
		var parent string
		if err := json.Unmarshal(raw, &parent); err != nil {
			continue
		}
		if parent = strings.TrimSpace(parent); parent != "" {
			return parent, true
		}
	}
	return "", false
}
