package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// fallingStore leaves three conversations, each with translation rows: a, its
// tail fork b, and c, cut at LT 4 and placed under b, whose stored chain
// falls (0, 13, 5).
func fallingStore(t *testing.T) (root, a, b, c string) {
	t.Helper()
	root = t.TempDir()
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err = be.ForkWith("", 0, birth("a"))
	if err != nil {
		t.Fatal(err)
	}
	aLog, _ := be.OpenFigIR(a)
	for i := 0; i < 10; i++ {
		put(t, aLog, ask(uint64(i+1), fmt.Sprintf("a%d", i)))
	}
	if b, _, err = be.ForkWith(a, 0, birth("b")); err != nil {
		t.Fatal(err)
	}
	bLog, _ := be.OpenFigIR(b)
	for i := 0; i < 5; i++ {
		put(t, bLog, ask(uint64(20+i), "b"))
	}
	if c, _, err = be.ForkWith(b, 4, birth("c")); err != nil {
		t.Fatal(err)
	}
	if err := be.Store().move(c, b); err != nil {
		t.Fatal(err)
	}
	cLog, _ := be.OpenFigIR(c)
	for i := 0; i < 5; i++ {
		put(t, cLog, ask(uint64(30+i), "c"))
	}
	for _, id := range []string{a, b, c} {
		tail, _ := mustIR(t, be, id).PeekTail()
		if _, err := be.store.trunks.Append(id, transChannel("anthropic"), tail.LT, []byte(`{"row":true}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	return root, a, b, c
}

func mustIR(t *testing.T, be *XwalBackend, id string) Log[message.Message] {
	t.Helper()
	l, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestMisreadLineagesListsOnlyFallingChains(t *testing.T) {
	root, _, _, c := fallingStore(t)
	nodes, err := MisreadLineages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Trunk != c {
		raw, _ := json.Marshal(nodes)
		t.Fatalf("misread nodes %s, want only %s", raw, c)
	}
	if got := nodes[0].Channels; len(got) != 1 || got[0] != transChannel("anthropic") {
		t.Fatalf("derived channels %v, want [%s]", got, transChannel("anthropic"))
	}
}

// The v4 -> v5 open clears the derived rows of a falling chain and nothing
// else: its rows were encoded from another conversation, its neighbours' were
// not, and no IR is touched.
func TestTranslationsV5ClearsOnlyMisreadNodes(t *testing.T) {
	root, a, b, c := fallingStore(t)
	irBefore := map[string]uint64{}
	for _, id := range []string{a, b, c} {
		irBefore[id], _ = channelLast(t, root, id, chanIR)
		if last, ok := channelLast(t, root, id, transChannel("anthropic")); !ok || last == 0 {
			t.Fatalf("fixture: %s has no translation row", id)
		}
	}
	putSchema(t, root, map[string]int{"translations-v2/": 4, chanIR: 5, chanForm: 2, chanUI: 1})
	s, err := OpenXwalStore(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.trunks.Close()
	for id, wantCleared := range map[string]bool{a: false, b: false, c: true} {
		last, _ := channelLast(t, root, id, transChannel("anthropic"))
		if (last == 0) != wantCleared {
			t.Errorf("%s: translation last %d, cleared want %v", id, last, wantCleared)
		}
		if ir, _ := channelLast(t, root, id, chanIR); ir != irBefore[id] {
			t.Errorf("%s: IR moved from %d to %d", id, irBefore[id], ir)
		}
	}
	f, err := readSchema(root)
	if err != nil {
		t.Fatal(err)
	}
	if f.Channels["translations-v2/"] != 5 {
		t.Fatalf("translations stamped v%d, want 5", f.Channels["translations-v2/"])
	}
}
