package figaro

import (
	"fmt"
	"testing"
	"unsafe"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// THE HAZARD, pinned before the seed that could violate it.
//
// A spliced materialization must equal a wholesale one, turn for turn and node
// for node. A donation that is not really a prefix does not make the pager
// slow; it makes the child render the ANCESTOR'S history as its own, which is
// live/committed divergence with a different cause.
//
// The oracle is the wholesale composition of the same entries, kept
// permanently: an equivalence claim is a fact about every day after the
// change, and it stays checkable only while both sides are present.

// seedFixture builds turn-structured entries carrying BOTH node kinds. Prose
// passes through composition and aliases; a tool node is rebuilt. A fixture of
// prose alone reports sharing that most composed bytes never get -- the fault
// that made phase 4's first measurement wrong twice.
func seedFixture(turns int) []store.Entry[message.Message] {
	long := ""
	for i := 0; i < 200; i++ {
		long += fmt.Sprintf("tool output line %d, long enough to be worth sharing\n", i)
	}
	var out []store.Entry[message.Message]
	lt := uint64(0)
	add := func(role message.Role, turn uint64, c ...message.Content) {
		lt++
		out = append(out, store.Entry[message.Message]{
			FigaroLT: lt, LT: lt,
			Payload: message.Message{Role: role, TurnID: turn, Content: c},
		})
	}
	for i := 1; i <= turns; i++ {
		turn := uint64(i)
		call := fmt.Sprintf("call-%d", i)
		add(message.RoleInput, turn, message.TextContent(fmt.Sprintf("question %d, long enough to be worth sharing between branches", i)))
		add(message.RoleOutput, turn,
			message.TextContent(fmt.Sprintf("answer %d, also long enough to be worth sharing", i)),
			message.Content{Type: message.ContentToolInvoke, ToolCallID: call, ToolName: "bash",
				Arguments: map[string]any{"command": "echo hi"}},
		)
		add(message.RoleInput, turn, message.ToolResultContent(call, "bash", long, false))
	}
	return out
}

func seedCompose(entries []store.Entry[message.Message]) []aria.Turn {
	return uiir.New(nil).Turns(unwrapMessages(entries))
}

func nodeStrings(n livedoc.Node) []string {
	var out []string
	for _, s := range []string{n.Markdown, n.Input, n.Output, n.Summary} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func sameStringData(a, b string) bool {
	return len(a) == len(b) && unsafe.StringData(a) == unsafe.StringData(b)
}

func requireSameTurns(t *testing.T, what string, want, got []aria.Turn) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: wholesale composed %d turns, spliced composed %d", what, len(want), len(got))
	}
	for i := range want {
		if want[i].ID != got[i].ID {
			t.Fatalf("%s: turn %d: wholesale id %d, spliced id %d", what, i, want[i].ID, got[i].ID)
		}
		if len(want[i].Nodes) != len(got[i].Nodes) {
			t.Fatalf("%s: turn %d (id %d): wholesale has %d nodes, spliced has %d",
				what, i, want[i].ID, len(want[i].Nodes), len(got[i].Nodes))
		}
		for j := range want[i].Nodes {
			w, g := nodeStrings(want[i].Nodes[j]), nodeStrings(got[i].Nodes[j])
			if len(w) != len(g) {
				t.Fatalf("%s: turn %d node %d: %d strings vs %d", what, i, j, len(w), len(g))
			}
			for k := range w {
				if w[k] != g[k] {
					t.Fatalf("%s: turn %d node %d string %d DIFFERS:\n wholesale: %.80q\n spliced:   %.80q",
						what, i, j, k, w[k], g[k])
				}
			}
		}
	}
}

// EVERY LEGAL SEAM, not one: the donation may cover any number of whole turns.
func TestSplicedMaterializationEqualsWholesaleAtEverySeam(t *testing.T) {
	entries := seedFixture(8)
	wholesale := seedCompose(entries)
	if len(wholesale) != 8 {
		t.Fatalf("fixture composed %d turns, want 8", len(wholesale))
	}
	kinds := map[string]int{}
	for _, tn := range wholesale {
		for _, n := range tn.Nodes {
			kinds[string(n.Type)]++
		}
	}
	if kinds["prose"] == 0 || kinds["tool"] == 0 {
		t.Fatalf("fixture is not representative: node kinds %v, need both prose and tool", kinds)
	}

	for n := 1; n <= len(wholesale); n++ {
		donated := seedCompose(entries)[:n] // an ancestor's own composition
		got, ok := spliceDonated(donated, entries, func(_ int, rest []store.Entry[message.Message]) []aria.Turn {
			return seedCompose(rest)
		})
		if !ok {
			t.Fatalf("a donation of %d whole turns was refused; it is a prefix by construction", n)
		}
		requireSameTurns(t, fmt.Sprintf("seam after %d turns", n), wholesale, got)
	}
}

// THE MEASUREMENT, by identity: the donated prefix must be SHARED, not minted.
// Phase 4's number for two independent compositions was 120 compared, 40
// shared, 80 minted, and the minted ones were the tool Input/Output that
// dominate by bytes.
func TestSplicedMaterializationSharesTheAncestorsNodeStrings(t *testing.T) {
	entries := seedFixture(8)
	ancestor := seedCompose(entries) // what the parent already holds

	// BEFORE: the child composes the same history for itself.
	independent := seedCompose(entries)
	compared, shared := 0, 0
	for i := range ancestor {
		for j := range ancestor[i].Nodes {
			a, b := nodeStrings(ancestor[i].Nodes[j]), nodeStrings(independent[i].Nodes[j])
			for k := range a {
				compared++
				if sameStringData(a[k], b[k]) {
					shared++
				}
			}
		}
	}
	t.Logf("BEFORE (two independent compositions): %d strings compared, %d SHARED, %d MINTED",
		compared, shared, compared-shared)
	if compared == 0 {
		t.Fatal("compared nothing; the fixture cannot prove anything")
	}
	// The fixture must be able to report minting, or "shared" means nothing.
	if shared == compared {
		t.Fatal("two independent compositions already share every string; this meter cannot see the fix")
	}

	// AFTER: the child splices the ancestor's turns for the shared prefix.
	const donatedTurns = 6
	got, ok := spliceDonated(ancestor[:donatedTurns], entries, func(_ int, rest []store.Entry[message.Message]) []aria.Turn {
		return seedCompose(rest)
	})
	if !ok {
		t.Fatal("the donation was refused")
	}
	compared2, shared2 := 0, 0
	for i := 0; i < donatedTurns; i++ {
		for j := range ancestor[i].Nodes {
			a, b := nodeStrings(ancestor[i].Nodes[j]), nodeStrings(got[i].Nodes[j])
			for k := range a {
				compared2++
				if sameStringData(a[k], b[k]) {
					shared2++
				}
			}
		}
	}
	t.Logf("AFTER (spliced from the ancestor): %d strings compared, %d SHARED, %d MINTED",
		compared2, shared2, compared2-shared2)
	if shared2 != compared2 {
		t.Errorf("%d of %d donated strings were MINTED; the splice is not sharing the ancestor's nodes",
			compared2-shared2, compared2)
	}
}

// A donation that is NOT a prefix must be refused, not spliced. Each of these
// is a way the ancestor and the child can disagree about history.
func TestADonationThatIsNotAPrefixIsRefused(t *testing.T) {
	entries := seedFixture(6)
	full := seedCompose(entries)

	cases := []struct {
		name    string
		donated []aria.Turn
		entries []store.Entry[message.Message]
	}{
		{"empty donation", nil, entries},
		{"turn ids not ascending", []aria.Turn{full[2], full[1]}, entries},
		{"a turn the log does not contain", []aria.Turn{{ID: 99}}, entries},
		{"more donated turns than the log holds below the seam",
			append(append([]aria.Turn{}, full[:3]...), aria.Turn{ID: 3}), entries},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := spliceDonated(tc.donated, tc.entries, func(_ int, rest []store.Entry[message.Message]) []aria.Turn {
				return seedCompose(rest)
			}); ok {
				t.Fatalf("a donation that is not a prefix was ACCEPTED; the child would render another aria's history as its own")
			}
		})
	}
}

// PROVE THE REFUSALS CAN PASS. A guard that refuses everything is as useless
// as one that refuses nothing, and it would silently turn the seed off.
func TestALegalDonationIsAccepted(t *testing.T) {
	entries := seedFixture(6)
	full := seedCompose(entries)
	if _, ok := spliceDonated(full[:3], entries, func(_ int, rest []store.Entry[message.Message]) []aria.Turn {
		return seedCompose(rest)
	}); !ok {
		t.Fatal("a legal three-turn donation was refused; the guards refuse everything and the seed is dead code")
	}
}

// THE DONOR HALF. A turn straddling the fork base must never be offered: the
// splice is sound only over WHOLE turns, and half a turn is precisely the
// shape composition is not local over.
func TestTurnsBelowOffersOnlyWholeTurnsUnderTheBase(t *testing.T) {
	a := &Agent{ariaSrv: aria.NewServer()}
	mk := func(id uint64, lts ...uint64) aria.Turn {
		var nodes []livedoc.Node
		for _, lt := range lts {
			nodes = append(nodes, livedoc.Node{Type: livedoc.NodeProse, LTs: []uint64{lt},
				Markdown: fmt.Sprintf("turn %d at lt %d", id, lt)})
		}
		return aria.Turn{ID: id, Nodes: nodes}
	}
	for _, tn := range []aria.Turn{mk(1, 1, 2, 3), mk(2, 4, 5, 6), mk(3, 7, 8, 9)} {
		a.ariaSrv.Commit(tn)
	}

	for _, tc := range []struct {
		base uint64
		want int
	}{
		{base: 0, want: 0}, // no base: no donation
		{base: 4, want: 1}, // turn 2 begins at 4
		{base: 5, want: 1}, // STRADDLES: turn 2 holds 4,5,6
		{base: 7, want: 2},
		{base: 99, want: 3},
	} {
		if got := len(a.TurnsBelow(tc.base)); got != tc.want {
			t.Errorf("TurnsBelow(%d) offered %d turns, want %d; a straddling turn donated as a whole one "+
				"would hand the child nodes for records it does not own", tc.base, got, tc.want)
		}
	}
}
