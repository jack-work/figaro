package store

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/turns"
)

// The fork base turn: computed locally, recorded at birth, believed only for
// the base it names. See turnat.go and recordForkTurn.

func birth(id string) message.Patch {
	raw, _ := json.Marshal(id)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": raw}, nil)
}

func newConversation(t testing.TB) (*XwalBackend, string, Log[message.Message]) {
	t.Helper()
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { be.Close() })
	id, _, err := be.ForkWith("", 0, birth("p"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	return be, id, log
}

func put(t testing.TB, log Log[message.Message], m message.Message) uint64 {
	t.Helper()
	e, err := log.Append(Entry[message.Message]{Payload: m})
	if err != nil {
		t.Fatal(err)
	}
	return e.LT
}

func ask(turn uint64, text string) message.Message {
	return message.Message{Role: message.RoleInput, TurnID: turn, Content: []message.Content{message.TextContent(text)}}
}

func answer(turn uint64, text string) message.Message {
	return message.Message{Role: message.RoleOutput, TurnID: turn, Content: []message.Content{message.TextContent(text)}}
}

// THE LOCAL READ AGREES WITH THE DEFINITION, everywhere. turns.StampIDs over
// the whole log is what "the turn that owns lt" means; TurnAtLT must give the
// same answer from a record or two.
//
// The logs are fork chains, because that is where the interesting records
// are: every fork writes a raw birth record (turn 0) at its base, in the
// middle of a log whose neighbours are stamped. Interior and head forks at
// random points, more turns written in each child, tool-shaped rounds and
// ceremonial records between and inside turns. Every LT from 1 to past the
// end of every aria in the chain is asked.
func TestTurnAtLTAgreesWithStampIDs(t *testing.T) {
	for seed := int64(1); seed <= 30; seed++ {
		rng := rand.New(rand.NewSource(seed))
		be, cur, log := newConversation(t)
		chain := []string{cur}
		turn := uint64(0)
		write := func(l Log[message.Message], n int) {
			for i := 0; i < n; i++ {
				turn++
				put(t, l, ask(turn, "q"))
				for r := 0; r < rng.Intn(4); r++ {
					if rng.Intn(2) == 0 {
						put(t, l, answer(turn, "thinking"))
					} else {
						put(t, l, message.Message{Role: message.RoleInput}) // ceremonial
					}
				}
				put(t, l, answer(turn, "a"))
			}
		}
		write(log, 2+rng.Intn(4))
		for hop := 0; hop < 1+rng.Intn(4); hop++ {
			at := uint64(0) // head
			if rng.Intn(2) == 0 {
				tail, _ := log.ReadPage(0, 1<<62, 1)
				at = 1 + uint64(rng.Int63n(int64(tail[0].LT)))
			}
			child, _, err := be.ForkWith(cur, at, birth("c"))
			if err != nil {
				t.Fatal(err)
			}
			cur = child
			chain = append(chain, cur)
			log, _ = be.OpenFigIR(cur)
			// Sometimes a child writes nothing: its tail is its birth record.
			if rng.Intn(3) != 0 {
				turn = lastTurn(t, log)
				write(log, 1+rng.Intn(3))
			}
		}
		for _, id := range chain {
			node := be.OpenNode(id)
			all := readAll(node)
			if len(all) == 0 {
				continue
			}
			for lt := uint64(1); lt <= all[len(all)-1].LogicalTime+3; lt++ {
				gotT, gotE := TurnAtLT(node, lt)
				wantT, wantE := definition(all, lt)
				if gotT != wantT || gotE != wantE {
					t.Fatalf("seed %d aria %s lt %d: TurnAtLT says (%d, exact=%v), StampIDs says (%d, exact=%v)",
						seed, id, lt, gotT, gotE, wantT, wantE)
				}
			}
		}
	}
}

// definition is the owner of lt under turns.StampIDs, read off the whole log.
func definition(stamped []message.Message, lt uint64) (uint64, bool) {
	var turn uint64
	exact := false
	for _, m := range stamped {
		if m.LogicalTime > lt {
			break
		}
		turn, exact = m.TurnID, m.LogicalTime == lt
	}
	return turn, exact
}

// readAll is the log, every record, stamped the way StampIDs stamps it.
func readAll(l Log[message.Message]) []message.Message {
	var msgs []message.Message
	for from := uint64(1); ; {
		got := l.ReadFrom(from, 512)
		if len(got) == 0 {
			break
		}
		for _, e := range got {
			m := e.Payload
			m.LogicalTime = e.LT
			msgs = append(msgs, m)
		}
		from = got[len(got)-1].LT + 1
	}
	turns.StampIDs(msgs)
	return msgs
}

func lastTurn(t *testing.T, l Log[message.Message]) uint64 {
	t.Helper()
	all := readAll(l)
	if len(all) == 0 {
		return 0
	}
	return all[len(all)-1].TurnID
}

// A FORK RECORDS ITS BASE TURN AT BIRTH, in the parent's turn space, and says
// whether it was exact. Interior forks land on a real record of the parent;
// a head fork lands past the parent's end and is recorded conservatively.
func TestForkWithRecordsTheBaseTurn(t *testing.T) {
	be, parent, log := newConversation(t)
	var firstOf [4]uint64
	for turn := uint64(1); turn <= 3; turn++ {
		firstOf[turn] = put(t, log, ask(turn, "q"))
		put(t, log, answer(turn, "a"))
	}

	cases := []struct {
		name      string
		at        uint64
		wantTurn  uint64
		wantExact bool
	}{
		{"head", 0, 3, false},
		{"interior at turn 2", firstOf[2] - 1, 2, true},
		{"interior at turn 3", firstOf[3] - 1, 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			child, _, err := be.ForkWith(parent, c.at, birth(c.name))
			if err != nil {
				t.Fatal(err)
			}
			links := be.LineageLinks(child)
			l := links[len(links)-1]
			if !l.BaseTurnKnown || l.BaseTurn != c.wantTurn || l.BaseTurnExact != c.wantExact {
				t.Fatalf("recorded %+v, want turn %d exact=%v", l, c.wantTurn, c.wantExact)
			}
			// And it is what a log read would have said, which is the whole
			// claim: the number is a cache of the read, never a different answer.
			gotT, gotE := TurnAtLT(be.OpenNode(parent), l.BaseLT)
			if gotT != l.BaseTurn || gotE != l.BaseTurnExact {
				t.Fatalf("recorded (%d, %v) but the parent's log says (%d, %v)", l.BaseTurn, l.BaseTurnExact, gotT, gotE)
			}
		})
	}
}

// A FORK OF A FORK reads its base turn through the parent's inherited prefix:
// a parent that has written nothing of its own still answers from the turns
// it shares with ITS parent.
func TestForkOfAFreshForkRecordsTheInheritedTurn(t *testing.T) {
	be, parent, log := newConversation(t)
	for turn := uint64(1); turn <= 4; turn++ {
		put(t, log, ask(turn, "q"))
		put(t, log, answer(turn, "a"))
	}
	mid, _, err := be.ForkWith(parent, 0, birth("mid"))
	if err != nil {
		t.Fatal(err)
	}
	leaf, _, err := be.ForkWith(mid, 0, birth("leaf"))
	if err != nil {
		t.Fatal(err)
	}
	links := be.LineageLinks(leaf)
	if len(links) < 3 {
		t.Fatalf("lineage %+v, want parent, mid, leaf at least", links)
	}
	l := links[len(links)-1]
	if !l.BaseTurnKnown || l.BaseTurn != 4 {
		t.Fatalf("leaf recorded %+v, want turn 4 (the last turn mid inherited)", l)
	}
}

// A sidecar is believed only for the base it names, and never when torn.
// Either way the store keeps working, it just does not know the turn.
func TestForkTurnSidecarIsSelfValidating(t *testing.T) {
	be, parent, log := newConversation(t)
	for turn := uint64(1); turn <= 2; turn++ {
		put(t, log, ask(turn, "q"))
		put(t, log, answer(turn, "a"))
	}
	child, _, err := be.ForkWith(parent, 0, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := be.store.trunks.InfoLight(child)
	sidecar := onlySidecar(t, be.store.root)

	for _, bad := range []string{
		"base=999\nturn=2\nexact=true\n", // a base this node does not have
		"base=",                          // torn
		"",                               // empty
		"turn=2\n",                       // no base at all
	} {
		if err := os.WriteFile(sidecar, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := be.store.trunks.InfoLight(child)
		if !ok || got.BranchedTurnKnown {
			t.Fatalf("sidecar %q was believed: %+v", bad, got)
		}
		if got.BranchedLT != info.BranchedLT {
			t.Fatalf("a bad sidecar changed the base itself: %d, want %d", got.BranchedLT, info.BranchedLT)
		}
	}
	// And a wrong base is refused at write time too.
	if err := be.RecordBaseTurn(child, info.BranchedLT+7, 2, true); err == nil {
		t.Fatal("recorded a turn against a base the node does not have")
	}
	// The right one is accepted and survives a reopen.
	if err := be.RecordBaseTurn(child, info.BranchedLT, 2, true); err != nil {
		t.Fatal(err)
	}
	dir := be.store.root
	be.Close()
	again, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	got, _ := again.store.trunks.InfoLight(child)
	if !got.BranchedTurnKnown || got.BranchedTurn != 2 || !got.BranchedTurnExact {
		t.Fatalf("after reopen: %+v", got)
	}
}

// THE ANCESTRY WALK IS THE SAME WALK, just without listing the store. Built
// against the listing it replaced, over a tree with siblings, cousins and
// unrelated arias beside it.
func TestLineageLinksMatchesTheListingWalk(t *testing.T) {
	be, root, log := newConversation(t)
	for turn := uint64(1); turn <= 3; turn++ {
		put(t, log, ask(turn, "q"))
		put(t, log, answer(turn, "a"))
	}
	ids := []string{root}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 12; i++ {
		parent := ids[rng.Intn(len(ids))]
		child, _, err := be.ForkWith(parent, 0, birth("x"))
		if err != nil {
			t.Fatal(err)
		}
		cl, _ := be.OpenFigIR(child)
		put(t, cl, ask(uint64(10+i), "q"))
		ids = append(ids, child)
	}
	for i := 0; i < 5; i++ { // unrelated arias
		if _, _, err := be.ForkWith("", 0, birth("u")); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		got := be.LineageLinks(id)
		want := listingWalk(be, id)
		if len(got) != len(want) {
			t.Fatalf("%s: %d links, the listing walk has %d", id, len(got), len(want))
		}
		for i := range got {
			if got[i].Node != want[i].Node || got[i].Kind != want[i].Kind || got[i].BaseLT != want[i].BaseLT {
				t.Fatalf("%s link %d: %+v, the listing walk has %+v", id, i, got[i], want[i])
			}
		}
	}
}

// listingWalk is LineageLinks as it was: list every trunk, then walk.
func listingWalk(be *XwalBackend, id string) []Link {
	infos := be.store.trunks.ListLight()
	by := map[string]int{}
	for i, t := range infos {
		by[t.ID] = i
	}
	var links []Link
	seen := map[string]bool{}
	for cur := id; cur != ""; {
		i, ok := by[cur]
		if !ok || seen[cur] {
			break
		}
		seen[cur] = true
		t := infos[i]
		links = append(links, Link{Node: t.ID, Kind: t.Kind, BaseLT: t.BranchedLT})
		cur = t.Parent
	}
	for l, r := 0, len(links)-1; l < r; l, r = l+1, r-1 {
		links[l], links[r] = links[r], links[l]
	}
	if len(links) > 0 {
		links[0].BaseLT = 0
	}
	return links
}

// onlySidecar finds the one .forkturn in a store that has exactly one fork.
func onlySidecar(t *testing.T, root string) string {
	t.Helper()
	var found []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == ".forkturn" {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 1 {
		t.Fatalf("want exactly one .forkturn under %s, found %v", root, found)
	}
	return found[0]
}

// READPAGE BY POSITION IS THE SAME PAGE AS THE WHOLE-LOG SLICE. The raw log's
// ReadPage used to decode every record and slice; it reads only the window
// now. Every (from, before, n) shape is asked of every aria in random fork
// chains (whose prefixes are read through their parents), raw and through the
// aria's own log, and compared with readPage over Read().
func TestReadPageByPositionMatchesTheSlice(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		rng := rand.New(rand.NewSource(seed))
		be, cur, log := newConversation(t)
		chain := []string{cur}
		turn := uint64(0)
		for hop := 0; hop < 3; hop++ {
			for i := 0; i < 2+rng.Intn(5); i++ {
				turn++
				put(t, log, ask(turn, "q"))
				put(t, log, answer(turn, "a"))
			}
			at := uint64(0)
			if rng.Intn(2) == 0 {
				tail, _ := log.ReadPage(0, 1<<62, 1)
				at = 1 + uint64(rng.Int63n(int64(tail[0].LT)))
			}
			child, _, err := be.ForkWith(cur, at, birth("c"))
			if err != nil {
				t.Fatal(err)
			}
			cur = child
			chain = append(chain, cur)
			log, _ = be.OpenFigIR(cur)
		}
		for _, id := range chain {
			aria, _ := be.OpenFigIR(id)
			for name, l := range map[string]Log[message.Message]{"raw": be.OpenNode(id), "aria": aria} {
				all := l.Read()
				if len(all) == 0 {
					continue
				}
				top := all[len(all)-1].LT
				for q := 0; q < 200; q++ {
					from, before, n := uint64(0), uint64(0), rng.Intn(6)
					switch rng.Intn(4) {
					case 0:
						before = 1 + uint64(rng.Int63n(int64(top+3)))
					case 1:
						before = ^uint64(0)
					case 2:
						from = uint64(rng.Int63n(int64(top + 3)))
					case 3:
						from = 0
					}
					got, _ := l.ReadPage(from, before, n)
					want, _ := readPage(all, from, before, n)
					if len(got) != len(want) {
						t.Fatalf("seed %d %s %s ReadPage(%d, %d, %d): %d entries, the slice has %d",
							seed, name, id, from, before, n, len(got), len(want))
					}
					for i := range got {
						if got[i].LT != want[i].LT || got[i].Payload.TurnID != want[i].Payload.TurnID {
							t.Fatalf("seed %d %s %s ReadPage(%d, %d, %d)[%d]: LT %d turn %d, the slice has LT %d turn %d",
								seed, name, id, from, before, n, i, got[i].LT, got[i].Payload.TurnID, want[i].LT, want[i].Payload.TurnID)
						}
					}
				}
			}
		}
	}
}
