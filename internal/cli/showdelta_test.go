package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// `show` draws the same table the pager does, collapsed, and -v opens it.
func TestShowDrawsTheDeltaTable(t *testing.T) {
	deltas := map[string]livedoc.FormDelta{
		"a1.system.forked_from": {Value: json.RawMessage(`"aaaa1111"`), Kind: livedoc.FormBound, Event: livedoc.FormSet, Form: "a1"},
		"a1.mantra":             {Value: json.RawMessage(`"` + strings.Repeat("m", 60) + `"`), Kind: livedoc.FormBound, Event: livedoc.FormSet, Form: "a1"},
	}
	m := aria.Message{
		Turn: 2, Role: livedoc.RoleOutput, Inquiry: "go", FormDeltas: deltas,
		Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "done"}},
	}
	collapsed := strings.Join(renderTurnRows(m, 100, 0, renderSettings{}), "\n")
	if !strings.Contains(stripANSI(collapsed), "⑂ aaaa1111") {
		t.Fatalf("show must draw the fork banner:\n%s", collapsed)
	}
	if strings.Contains(stripANSI(collapsed), strings.Repeat("m", 60)) {
		t.Fatalf("show collapses the values:\n%s", collapsed)
	}
	open := strings.Join(renderTurnRows(m, 100, 0, renderSettings{verbose: true}), "\n")
	if !strings.Contains(stripANSI(open), strings.Repeat("m", 60)) {
		t.Fatalf("show -v opens the table:\n%s", open)
	}
}
