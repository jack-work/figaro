package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

func bigNode(n int) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: strings.Repeat("x", n)}
}

func sealedTurnPage(id uint64, from uint64, nodes []livedoc.Node) aria.Page {
	return aria.Page{Parts: []aria.TurnPart{{
		Turn: aria.Turn{ID: id, Sealed: true, Nodes: nodes},
		From: from,
	}}}
}

// The immutable-backpage property: a page below the live suffix can never
// receive a delta, so re-fetching it must reproduce it exactly.
func TestPageMessages_RefetchIsIdentical(t *testing.T) {
	page := sealedTurnPage(3, 0, []livedoc.Node{bigNode(40000), bigNode(40000), bigNode(10)})
	a, err := json.Marshal(pageMessages(page))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(pageMessages(page))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("re-fetching an immutable page produced different units")
	}
}

// A part that is itself a slice of a turn keeps its wire offset, so a unit's
// From is its true coordinate in the turn and not merely its index in the page.
func TestPageMessages_HonoursPartOffset(t *testing.T) {
	got := pageMessages(sealedTurnPage(9, 5, []livedoc.Node{bigNode(4), bigNode(4)}))
	if len(got) != 1 || got[0].From != 5 {
		t.Fatalf("unit From = %v, want 5, a clipped part starts where the wire says", got)
	}
}

// Real data, not a fixture: every unit the pager builds from the largest aria
// on this machine must fit the budget, whatever the turns do.
func TestUnits_RealAriaAreBounded(t *testing.T) {
	path := os.Getenv("BIG_IR")
	if path == "" {
		t.Skip("set BIG_IR to a real .jsonl to run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []message.Message
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var env struct {
			M uint64          `json:"m"`
			P message.Message `json:"p"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}
		env.P.LogicalTime = env.M
		msgs = append(msgs, env.P)
	}
	turns := compose.Turns(msgs)
	if len(turns) == 0 {
		t.Fatal("no turns parsed")
	}
	worstTurn, worstUnit, units := 0, 0, 0
	for _, tn := range turns {
		if n := len(renderNodeList(tn.Nodes, 100, 0, renderSettings{})); n > worstTurn {
			worstTurn = n
		}
		for _, m := range pageMessages(sealedTurnPage(tn.ID, 0, tn.Nodes)) {
			units++
			if n := len(renderNodeList(m.Nodes, 100, 0, renderSettings{})); n > worstUnit {
				worstUnit = n
			}
		}
	}
	t.Logf("turns=%d units=%d tallest turn=%d rows tallest unit=%d rows (window budget=%d)",
		len(turns), units, worstTurn, worstUnit, transcriptWindowRows)
	if worstUnit > transcriptWindowRows {
		t.Fatalf("a single unit (%d rows) still exceeds the whole retained window (%d)",
			worstUnit, transcriptWindowRows)
	}
}
