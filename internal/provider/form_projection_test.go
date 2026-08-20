package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// fakeBoard is the agent's patchCursor contract, standing alone: a forward
// walk over versioned patches answering an ABSOLUTE range.
type fakeBoard struct {
	versions []uint64
	i        int
}

func newFakeBoard(versions ...uint64) *fakeBoard { return &fakeBoard{versions: versions} }

func (b *fakeBoard) PatchesBetween(after, upTo uint64) []message.Patch {
	for b.i < len(b.versions) && b.versions[b.i] <= after {
		b.i++
	}
	var out []message.Patch
	for b.i < len(b.versions) && b.versions[b.i] <= upTo {
		out = append(out, message.Patch{Set: map[string]json.RawMessage{
			fmt.Sprintf("k%d", b.versions[b.i]): json.RawMessage(`1`),
		}})
		b.i++
	}
	return out
}

// stamp appends an entry carrying a form mark, the way a real IR
// record does.
func stamp(t *testing.T, log *store.MemLog[message.Message], text string, at uint64) {
	t.Helper()
	e, err := log.Append(store.Entry[message.Message]{
		FormChannelVersion: at,
		Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(text)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = e
}

// renderedKeys records which board keys each entry was given, so a test can
// compare two catch-ups by what the model would actually have seen. The rows
// log is the CURSOR: a second call over the same rows is a resume, which is
// what a warm start now means with no memo between calls.
func renderedKeys(log store.Log[message.Message], rows store.Log[[]json.RawMessage], board Form) ([]string, error) {
	var seen []string
	_, err := CatchUp(CatchUpConfig{
		Log:         log,
		Rows:        rows,
		Form:        board,
		Fingerprint: "v1",
		Encode: func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			for _, p := range msg.Patches {
				for k := range p.Set {
					seen = append(seen, k)
				}
			}
			return []json.RawMessage{json.RawMessage(`"x"`)}, nil
		},
	})
	return seen, err
}

// COLD EQUALS WARM. A catch-up resumes from the row log's tail, so the
// patches it renders must not depend on where it resumed. They did once: a
// fresh cursor pointed at the newest entry replayed the WHOLE board onto the
// first new message, and the stored row made that permanent -- every
// round-trip re-sent the aria's entire state.
func TestAResumedCatchUpRendersWhatAColdOneWould(t *testing.T) {
	build := func() *store.MemLog[message.Message] {
		log := store.NewMemLog[message.Message]()
		stamp(t, log, "one", 2)   // the outfit patch lands here
		stamp(t, log, "two", 4)   // two more patches
		stamp(t, log, "three", 5) // one more
		return log
	}

	coldKeys, err := renderedKeys(build(), store.NewMemLog[[]json.RawMessage](), newFakeBoard(2, 3, 4, 5))
	if err != nil {
		t.Fatal(err)
	}

	// The same log, caught up in two passes over ONE row log: the second
	// call resumes from the first call's tail.
	warmLog := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	stamp(t, warmLog, "one", 2)
	stamp(t, warmLog, "two", 4)
	warmKeys, err := renderedKeys(warmLog, rows, newFakeBoard(2, 3, 4, 5))
	if err != nil {
		t.Fatal(err)
	}
	stamp(t, warmLog, "three", 5)
	moreKeys, err := renderedKeys(warmLog, rows, newFakeBoard(2, 3, 4, 5))
	if err != nil {
		t.Fatal(err)
	}
	warm := append(append([]string{}, warmKeys...), moreKeys...)

	if strings.Join(warm, ",") != strings.Join(coldKeys, ",") {
		t.Errorf("a resume renders differently from a cold pass:\n cold %v\n warm %v", coldKeys, warm)
	}
	if len(coldKeys) == 0 {
		t.Error("a cold pass rendered no patches at all, so the comparison above proves nothing")
	}
}

// EXACTLY ONCE. Every patch reaches the model, and no patch reaches it
// twice: the two failure modes this seam has actually had, in that order.
func TestEveryPatchRendersExactlyOnceAcrossResumes(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	var all []string

	// Five turns, resuming each time, the way a tool loop drives it. A FRESH
	// board cursor every pass, because that is what formAccessor() does: it
	// is called per Send, not per turn.
	for i, mark := range []uint64{2, 3, 4, 6, 6} {
		stamp(t, log, fmt.Sprintf("turn%d", i), mark)
		keys, err := renderedKeys(log, rows, newFakeBoard(2, 3, 4, 5, 6))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, keys...)
	}

	counts := map[string]int{}
	for _, k := range all {
		counts[k]++
	}
	for _, want := range []string{"k2", "k3", "k4", "k5", "k6"} {
		switch counts[want] {
		case 1:
		case 0:
			t.Errorf("patch %s was never rendered: state the aria has and was never told about", want)
		default:
			t.Errorf("patch %s rendered %d times: the board is re-sent on every round-trip", want, counts[want])
		}
	}
}
