package store

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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

// A dry run pointed at the wrong path must fail, not report a clean store.
func TestMisreadLineagesRefusesAMissingStore(t *testing.T) {
	if _, err := MisreadLineages(t.TempDir() + "/nope"); err == nil {
		t.Fatal("a missing store listed as clean")
	}
}

// The counters behind the context percentage were folded through the old view;
// after the v5 open a misread node's sidecar equals a fresh fold of its IR,
// and a neighbour's is left as it was.
func TestTranslationsV5RefoldsMisreadMeta(t *testing.T) {
	root, a, _, c := fallingStore(t)
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	stale := &AriaMeta{MessageCount: 999, TurnCount: 77, TokensIn: 5, ContextTokens: 123456, LastFigaroLT: 1 << 20, Mantra: "kept"}
	for _, id := range []string{a, c} {
		if err := be.SetMeta(id, stale); err != nil {
			t.Fatal(err)
		}
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}
	putSchema(t, root, map[string]int{"translations-v2/": 4, chanIR: 5, chanForm: 2, chanUI: 1})
	if nodes, _ := MisreadLineages(root); len(nodes) != 1 || !nodes[0].Meta {
		t.Fatalf("dry run should name one node with a sidecar to refold, got %+v", nodes)
	}

	be, err = NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	ir := mustIR(t, be, c)
	var want AriaMeta
	ScanAll(ir, func(e Entry[message.Message]) bool { want.Fold(e); return true })
	got, err := be.Meta(c)
	if err != nil || got == nil {
		t.Fatalf("meta of %s: %v %v", c, got, err)
	}
	if got.Refold || got.MessageCount != want.MessageCount || got.TurnCount != want.TurnCount ||
		got.TokensIn != want.TokensIn || got.TokensOut != want.TokensOut ||
		got.ContextTokens != want.ContextTokens || got.LastFigaroLT != want.LastFigaroLT {
		t.Fatalf("refolded meta %+v, want the fresh fold %+v", got, want)
	}
	if got.Mantra != "kept" {
		t.Fatalf("refold dropped a non-counter field: mantra %q", got.Mantra)
	}
	mustIR(t, be, a)
	if other, _ := be.Meta(a); other == nil || other.MessageCount != 999 {
		t.Fatalf("a neighbour's sidecar was refolded: %+v", other)
	}
}

// A fork base the repair cannot read must not stop the open: every node whose
// chain passes through it is cleared as misread (derived rows only), named
// with the unreadable node, and the rest of the store is left as it was.
func TestAnUnreadableForkBaseOnTheChainCountsAsMisread(t *testing.T) {
	for name, corrupt := range map[string]func(path string) error{
		"malformed": func(p string) error { return os.WriteFile(p, []byte("base=x\n"), 0o644) },
		"empty":     func(p string) error { return os.WriteFile(p, nil, 0o644) },
		"deleted":   os.Remove,
	} {
		t.Run(name, func(t *testing.T) {
			root, a, b, c := fallingStore(t)
			if err := corrupt(irForkPath(t, root, b)); err != nil {
				t.Fatal(err)
			}
			nodes, err := MisreadLineages(root)
			if err != nil {
				t.Fatalf("dry run refused the store: %v", err)
			}
			got := map[string]string{}
			for _, n := range nodes {
				got[n.Trunk] = n.Reason
			}
			want := map[string]string{b: "unreadable .fork on " + b, c: "unreadable .fork on " + b}
			if !reflect.DeepEqual(got, want) {
				raw, _ := json.Marshal(nodes)
				t.Fatalf("misread nodes %s, want %v", raw, want)
			}

			putSchema(t, root, map[string]int{"translations-v2/": 4, chanIR: 5, chanForm: 2, chanUI: 1})
			s, err := OpenXwalStore(root, 0)
			if err != nil {
				t.Fatalf("the v5 open refused a store with an unreadable fork base: %v", err)
			}
			s.trunks.Close()
			for id, wantCleared := range map[string]bool{a: false, b: true, c: true} {
				last, _ := channelLast(t, root, id, transChannel("anthropic"))
				if (last == 0) != wantCleared {
					t.Errorf("%s: translation last %d, cleared want %v", id, last, wantCleared)
				}
			}
		})
	}
}

// The falling reason stays for a chain whose bases all read.
func TestAFallingChainIsNamedFalling(t *testing.T) {
	root, _, _, c := fallingStore(t)
	nodes, err := MisreadLineages(root)
	if err != nil || len(nodes) != 1 || nodes[0].Trunk != c || nodes[0].Reason != MisreadFalling {
		t.Fatalf("misread nodes %+v (%v), want %s as %q", nodes, err, c, MisreadFalling)
	}
}
