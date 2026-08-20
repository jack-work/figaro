package store

import (
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/turns"
)

func TestZZTurnMap(t *testing.T) {
	root := os.Getenv("TURNMAP_STORE")
	if root == "" {
		t.Skip("no TURNMAP_STORE")
	}
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	lg, err := be.OpenFigIR(os.Getenv("TURNMAP_ARIA"))
	if err != nil {
		t.Fatal(err)
	}
	entries := lg.ReadFrom(0, 0)
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	last := turns.StampIDs(msgs)
	t.Logf("aria has turns 1..%d over %d messages", last, len(msgs))
	for turn := uint64(1); turn <= last; turn++ {
		first, end, ok := turns.Span(msgs, turn)
		if !ok {
			continue
		}
		var opener string
		for _, m := range msgs {
			if m.LogicalTime == first {
				for _, c := range m.Content {
					if c.Text != "" {
						opener = strings.ReplaceAll(c.Text, "\n", " ")
						break
					}
				}
			}
		}
		if len(opener) > 68 {
			opener = opener[:68]
		}
		t.Logf("  turn %2d  LT %4d..%-4d  %s", turn, first, end, opener)
	}
}
