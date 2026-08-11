package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
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

func TestWindowSatisfiesAsksForOneMoreTurnThanItShows(t *testing.T) {
	base := showOpts{last: 10, from: -1, to: -1, before: -1}

	// Exactly the requested count is NOT enough: the oldest turn held may have
	// lost its prompt to the page boundary, so it is dropped before rendering.
	ten := showWindow{entries: turnEntries(1, 10, 1), total: 100}
	if windowSatisfies(ten, base) {
		t.Fatal("10 turns held must not satisfy a request for the last 10: the oldest may be partial")
	}
	eleven := showWindow{entries: turnEntries(1, 11, 1), total: 100}
	if !windowSatisfies(eleven, base) {
		t.Fatal("11 turns held answers the last 10")
	}

	// -a is satisfied by reaching the head and by nothing else. This is the
	// case the old code got wrong in the most expensive way.
	all := base
	all.all = true
	if windowSatisfies(showWindow{entries: turnEntries(1, 500, 1), total: 100000}, all) {
		t.Fatal("--all must walk to the head, not stop at a page")
	}
	if !windowSatisfies(showWindow{entries: turnEntries(1, 3, 1), total: 6, atHead: true}, all) {
		t.Fatal("--all is satisfied at the head")
	}

	// --from wants a turn strictly older than the one asked for, which is the
	// proof that the requested turn itself is whole.
	from := base
	from.from = 1900
	held := showWindow{entries: turnEntries(1900, 5, 1), total: 100000}
	if windowSatisfies(held, from) {
		t.Fatal("a window starting exactly at turn 1900 cannot prove 1900 is complete")
	}
	held = showWindow{entries: turnEntries(1899, 6, 1), total: 100000}
	if !windowSatisfies(held, from) {
		t.Fatal("holding turn 1899 proves 1900 is whole")
	}
}

func TestTrimPartialHeadKeepsTheTail(t *testing.T) {
	ts := []aria.Turn{{ID: 7}, {ID: 8}, {ID: 9}}
	if got := trimPartialHead(ts, true); len(got) != 3 {
		t.Fatalf("a window at the head keeps every turn, got %d", len(got))
	}
	got := trimPartialHead(ts, false)
	if len(got) != 2 || got[0].ID != 8 {
		t.Fatalf("a partial window drops its oldest turn, got %+v", got)
	}
	// Never to nothing: one turn is better than an empty screen.
	if len(trimPartialHead(ts[:1], false)) != 1 {
		t.Fatal("the last remaining turn must survive")
	}
}

// A legacy aria (no stored turn ids) read from the tail would be numbered
// from 1 at an arbitrary point, and `show` prints those numbers as the
// coordinate `fork <id>:<turn>` takes. Detecting that is what sends the
// caller back to the forward read.
func TestDerivedIDsSpotsAnUnnumberedAria(t *testing.T) {
	stored := turnEntries(40, 2, 1)
	if derivedIDs(stored) {
		t.Fatal("stored turn ids must not look derived")
	}
	legacy := []store.Entry[message.Message]{
		{LT: 1, Payload: msg(message.RoleInput, 0, "q")},
		{LT: 2, Payload: msg(message.RoleOutput, 0, "a")},
	}
	if !derivedIDs(legacy) {
		t.Fatal("a prompt with no stored turn id means the numbering was derived")
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
