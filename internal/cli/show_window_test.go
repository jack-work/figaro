package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

// The window walk, without a daemon. What is asserted here is the DECISION
// each step makes: when the walk has read enough, what it does with a turn cut
// in half by a page boundary, and when it must refuse to number turns at all.
//
// The failure these pin is not a crash. `show` used to read one 1000 entry
// page forward from LT 0 and report it as the whole aria: a 4000 message aria
// answered "499 turns", `-a` printed turns 1..499 as if that were everything,
// and `--from 1900` said "no turn in range". Every one of those is a confident
// wrong answer, which is worse than an error.

func msg(role message.Role, turnID uint64, text string) message.Message {
	return message.Message{
		Role:    role,
		TurnID:  turnID,
		Content: []message.Content{message.TextContent(text)},
	}
}

// turnEntries builds n whole turns (prompt + answer), numbered from firstID,
// with LTs starting at firstLT. Turn ids are STORED, as figaro.appendMsg
// stores them, which is what lets a tail window number itself correctly.
func turnEntries(firstID uint64, n int, firstLT uint64) []store.Entry[message.Message] {
	out := make([]store.Entry[message.Message], 0, n*2)
	lt := firstLT
	for i := 0; i < n; i++ {
		id := firstID + uint64(i)
		out = append(out, store.Entry[message.Message]{LT: lt, Payload: msg(message.RoleInput, id, "q")})
		lt++
		out = append(out, store.Entry[message.Message]{LT: lt, Payload: msg(message.RoleOutput, id, "a")})
		lt++
	}
	return out
}

// The page walk's decisions, without a daemon: when it has read enough,
// how it rejoins a turn the wire budget cut, and which slice the selector
// names. What it does NOT do any more is compose: the turns arrive whole
// from the api, so the "oldest turn may be missing its prompt" case that
// shaped the old IR walk cannot arise.

func turnsOf(firstID uint64, n int) []aria.Turn {
	out := make([]aria.Turn, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, aria.Turn{ID: firstID + uint64(i), Sealed: true})
	}
	return out
}

func TestTurnsSatisfyAsksForOneMoreTurnThanItShows(t *testing.T) {
	base := showOpts{last: 10, from: -1, to: -1, before: -1}

	if turnsSatisfy(showTurns{turns: turnsOf(1, 10)}, base) {
		t.Fatal("10 turns held must not satisfy a request for the last 10")
	}
	if !turnsSatisfy(showTurns{turns: turnsOf(1, 11)}, base) {
		t.Fatal("11 turns held answers the last 10")
	}

	// -a is satisfied by reaching the head and by nothing else. This is the
	// case the old walk got wrong in the most expensive way: it reported a
	// page as the whole aria.
	all := base
	all.all = true
	if turnsSatisfy(showTurns{turns: turnsOf(1, 500)}, all) {
		t.Fatal("--all must walk to the head, not stop at a page")
	}
	if !turnsSatisfy(showTurns{turns: turnsOf(1, 3), atHead: true}, all) {
		t.Fatal("--all is satisfied at the head")
	}

	// --before N counts only turns OLDER than N, and wants one spare.
	before := base
	before.before = 100
	held := showTurns{turns: append(turnsOf(90, 10), turnsOf(100, 5)...)}
	if turnsSatisfy(held, before) {
		t.Fatal("10 turns older than 100 must not satisfy a request for 10 before it")
	}
	held = showTurns{turns: append(turnsOf(89, 11), turnsOf(100, 5)...)}
	if !turnsSatisfy(held, before) {
		t.Fatal("11 turns older than 100 answers it")
	}
}

// A WIRE BUDGET CAN CUT A TURN IN HALF (TurnPart.From), and `show` draws
// turns. Rejoining the parts is the only assembly left on the client, and
// it must not mistake two adjacent whole turns for one split one.
func TestJoinPartsRebuildsASplitTurn(t *testing.T) {
	page := aria.Page{Parts: []aria.TurnPart{
		{Turn: aria.Turn{ID: 7, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "a"}}}, From: 0},
		{Turn: aria.Turn{ID: 7, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "b"}}}, From: 1},
		{Turn: aria.Turn{ID: 8, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "c"}}}, From: 0},
	}}
	got := joinParts(page)
	if len(got) != 2 {
		t.Fatalf("want 2 turns, got %d: %+v", len(got), got)
	}
	if len(got[0].Nodes) != 2 || got[0].Nodes[0].Markdown != "a" || got[0].Nodes[1].Markdown != "b" {
		t.Fatalf("the split turn was not rejoined in order: %+v", got[0])
	}
	if got[1].ID != 8 || len(got[1].Nodes) != 1 {
		t.Fatalf("the following turn was folded into its predecessor: %+v", got[1])
	}
}

func TestSelectShowRangeNamesTheSelectorsSlice(t *testing.T) {
	ts := turnsOf(10, 10) // turns 10..19
	base := showOpts{last: 3, from: -1, to: -1, before: -1}

	lo, hi := selectShowRange(ts, base)
	if ts[lo].ID != 17 || hi != 10 {
		t.Fatalf("last 3 is turns 17..19, got %d..%d", ts[lo].ID, ts[hi-1].ID)
	}

	all := base
	all.all = true
	if lo, hi = selectShowRange(ts, all); lo != 0 || hi != 10 {
		t.Fatalf("--all is everything held, got %d..%d", lo, hi)
	}

	from := base
	from.from, from.to = 12, 14
	lo, hi = selectShowRange(ts, from)
	if ts[lo].ID != 12 || ts[hi-1].ID != 14 {
		t.Fatalf("--from 12 --to 14, got %d..%d", ts[lo].ID, ts[hi-1].ID)
	}

	before := base
	before.before = 15
	lo, hi = selectShowRange(ts, before)
	if ts[lo].ID != 12 || ts[hi-1].ID != 14 {
		t.Fatalf("--before 15 with last 3, got %d..%d", ts[lo].ID, ts[hi-1].ID)
	}
}

// A budget clips the OLDEST end. `show` is how a tail gets snapshotted, so the
// newest turn is the one that must survive.
func TestClipToBudgetDropsTheOldestEnd(t *testing.T) {
	mk := func(id uint64, n int) aria.Turn {
		body := make([]byte, n)
		for i := range body {
			body[i] = 'x'
		}
		return aria.Turn{ID: id, Inquiry: string(body)}
	}
	ts := []aria.Turn{mk(1, 100), mk(2, 100), mk(3, 100)}

	kept, dropped := clipToBudget(ts, 0)
	if dropped != 0 || len(kept) != 3 {
		t.Fatal("no budget clips nothing")
	}
	kept, dropped = clipToBudget(ts, 150)
	if dropped != 2 || len(kept) != 1 || kept[0].ID != 3 {
		t.Fatalf("the newest turn must survive, got %d dropped, kept %+v", dropped, idsOf(kept))
	}
	// A single turn larger than the budget is still shown: refusing to print
	// anything would be a worse answer than printing the thing asked for.
	kept, _ = clipToBudget(ts[:1], 10)
	if len(kept) != 1 {
		t.Fatal("the last turn is never clipped away")
	}
}

func idsOf(ts []aria.Turn) []uint64 {
	out := make([]uint64, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

// composeTurns must read the STORED turn ids rather than renumber from the
// first entry it happens to hold. This is the property that makes a tail
// window addressable: the [N] `show` prints is the N `fork` takes.
func TestComposeTurnsKeepsStoredIDsInATailWindow(t *testing.T) {
	tail := turnEntries(1990, 3, 4000)
	ts := composeTurns(tail)
	if len(ts) != 3 {
		t.Fatalf("want 3 turns, got %d", len(ts))
	}
	if got := idsOf(ts); got[0] != 1990 || got[2] != 1992 {
		t.Fatalf("a tail window renumbered itself: %v", got)
	}
	// And the JSON the -j path emits carries the same ids.
	raw, err := json.Marshal(ts[0])
	if err != nil {
		t.Fatal(err)
	}
	var back aria.Turn
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != 1990 {
		t.Fatalf("turn id did not survive the wire: %d", back.ID)
	}
}

// THE WIRING, not the pieces.
//
// --max-bytes was declared in the registry, parsed by nobody, and reassembled
// by nobody, so `show --max-bytes 50` printed five turns and exited 0. Both
// unit tests for the clipper passed throughout, because a function nobody
// calls is a function that cannot fail. This walks the chain the argv walks:
// every flag the `show` command declares must survive reassembly in cli.go
// and land in showOpts.
func TestEveryShowFlagIsWiredThrough(t *testing.T) {
	opts := parseShowArgs([]string{"--max-bytes", "50", "--last", "7", "--from", "3", "--to", "9", "-o", "-j"})
	if opts.maxBytes != 50 {
		t.Fatalf("--max-bytes did not reach showOpts: %+v", opts)
	}
	if opts.last != 7 || opts.from != 3 || opts.to != 9 || !opts.details || !opts.jsonOut {
		t.Fatalf("a declared flag was dropped by the parser: %+v", opts)
	}

	// The reassembly half. cli.go hand-copies each parsed flag into the argv
	// renderAria re-parses, so a flag added to the registry and forgotten
	// there is silently ignored: exactly the bug above. Reading the source is
	// crude and it is the only place this coupling is visible.
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	showBlock := body[strings.Index(body, `Name:    "show",`):]
	showBlock = showBlock[:strings.Index(showBlock, `Name:    "send",`)]
	for _, flag := range []string{"details", "verbose", "literal", "all", "json", "from", "to", "before", "max-bytes", "last"} {
		if !strings.Contains(showBlock, `"`+flag+`"`) {
			t.Errorf("show declares %q but never reassembles it: renderAria will never see it", flag)
		}
	}
}
