package anthropic

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// pairing reports tool_result ids that no preceding tool_use declares -- the
// exact condition Anthropic rejects with "unexpected tool_use_id found in
// tool_result blocks", reproduced locally over assembled rows.
func pairing(t *testing.T, rows []json.RawMessage) (orphans []string) {
	t.Helper()
	declared := map[string]bool{}
	{
		for _, raw := range rows {
			var m struct {
				Role    string `json:"role"`
				Content []struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					ToolUseID string `json:"tool_use_id"`
				} `json:"content"`
			}
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			for _, c := range m.Content {
				switch c.Type {
				case "tool_use":
					declared[c.ID] = true
				case "tool_result":
					if !declared[c.ToolUseID] {
						orphans = append(orphans, c.ToolUseID)
					}
				}
			}
		}
	}
	return orphans
}

// THE INCIDENT, REPLAYED ON THE STORE IT HAPPENED TO. A daemon killed between
// the canonical fig IR append and the derived row left ede92072 with a
// tool_result whose tool_use had no translation, and every send 400'd. Point
// FIGARO_HOLE_STORE at a COPY of such a store to prove catch-up repairs it.
func TestCatchUpRepairsARealHole(t *testing.T) {
	root := os.Getenv("FIGARO_HOLE_STORE")
	aria := os.Getenv("FIGARO_HOLE_ARIA")
	if root == "" || aria == "" {
		t.Skip("set FIGARO_HOLE_STORE and FIGARO_HOLE_ARIA to run against a real store copy")
	}
	b, err := store.NewXwalBackend(root, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	figLog, err := b.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := b.OpenTranslator(aria, "anthropic")
	if err != nil {
		t.Fatal(err)
	}

	before, _ := provider.CollectRows(provider.TranslationRows(rows, 0))
	orphansBefore := pairing(t, before)
	t.Logf("before: %d rows, %d orphaned tool_result(s): %v", len(before), len(orphansBefore), orphansBefore)
	if len(orphansBefore) == 0 {
		t.Skip("this store copy carries no hole; nothing to repair")
	}

	a := &Anthropic{Model: "claude-opus-5", ReminderRenderer: "tag"}
	seq, err := a.catchUp(figLog, rows, nil, nil)
	if err != nil {
		t.Fatalf("catch up: %v", err)
	}
	after, _ := provider.CollectRows(seq)
	if orphans := pairing(t, after); len(orphans) != 0 {
		t.Fatalf("after catch-up %d tool_result(s) still have no tool_use: %v", len(orphans), orphans)
	}
	t.Logf("after: %d rows, 0 orphans", len(after))

	var invoked int
	for _, e := range figLog.Read() {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolInvoke {
				invoked++
			}
		}
	}
	t.Logf("fig IR declares %d tool invokes; all are paired in the rows", invoked)
}
