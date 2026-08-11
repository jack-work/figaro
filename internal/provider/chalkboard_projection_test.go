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
		ChalkVersion: at,
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
// compare two projections by what the model would actually have seen.
func renderedKeys(config ProjectionConfig[EncodedMessages]) (*IncrementalProjection[EncodedMessages], []string, error) {
	var seen []string
	config.Encode = func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
		for _, p := range msg.Patches {
			for k := range p.Set {
				seen = append(seen, k)
			}
		}
		return []json.RawMessage{json.RawMessage(`"x"`)}, nil
	}
	config.Append = AppendEncodedMessage
	proj, _, err := ProjectIncrementally(config)
	return proj, seen, err
}

// COLD EQUALS WARM. The projection warm-starts mid-log, so the patches it
// renders must not depend on where it resumed. They did: a fresh cursor
// pointed at the newest entry replayed the WHOLE board onto the first new
// message, and the per-LT cache made that permanent: every round-trip
// re-sent the aria's entire state.
func TestWarmProjectionRendersWhatAColdOneWould(t *testing.T) {
	build := func() *store.MemLog[message.Message] {
		log := store.NewMemLog[message.Message]()
		stamp(t, log, "one", 2)   // the outfit patch lands here
		stamp(t, log, "two", 4)   // two more patches
		stamp(t, log, "three", 5) // one more
		return log
	}

	cold, coldKeys, err := renderedKeys(ProjectionConfig[EncodedMessages]{
		Log: build(), Fingerprint: "v1", Form: newFakeBoard(2, 3, 4, 5),
	})
	if err != nil {
		t.Fatal(err)
	}

	// The same log, projected in two passes with a warm start between them.
	warmLog := store.NewMemLog[message.Message]()
	stamp(t, warmLog, "one", 2)
	stamp(t, warmLog, "two", 4)
	first, warmKeys, err := renderedKeys(ProjectionConfig[EncodedMessages]{
		Log: warmLog, Fingerprint: "v1", Form: newFakeBoard(2, 3, 4, 5),
	})
	if err != nil {
		t.Fatal(err)
	}
	stamp(t, warmLog, "three", 5)
	_, moreKeys, err := renderedKeys(ProjectionConfig[EncodedMessages]{
		Log: warmLog, Fingerprint: "v1", Form: newFakeBoard(2, 3, 4, 5),
		Previous: first,
	})
	if err != nil {
		t.Fatal(err)
	}
	warm := append(append([]string{}, warmKeys...), moreKeys...)

	if strings.Join(warm, ",") != strings.Join(coldKeys, ",") {
		t.Errorf("warm start renders differently from cold:\n cold %v\n warm %v", coldKeys, warm)
	}
	if cold.LastChalkVersion == 0 {
		t.Error("a cold projection reports LastChalkVersion 0; a warm resume would restart from the beginning")
	}
}

// EXACTLY ONCE. Every patch reaches the model, and no patch reaches it
// twice: the two failure modes this seam has actually had, in that order.
func TestEveryPatchRendersExactlyOnceAcrossResumes(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	var all []string

	// Five turns, resuming each time, the way a tool loop drives it.
	var prev *IncrementalProjection[EncodedMessages]
	for i, mark := range []uint64{2, 3, 4, 6, 6} {
		stamp(t, log, fmt.Sprintf("turn%d", i), mark)
		// A FRESH cursor every pass, because that is what chalkAccessor()
		// does: it is called per Send, not per turn. A test that reuses one
		// cursor cannot reproduce the bug this file exists for.
		proj, keys, err := renderedKeys(ProjectionConfig[EncodedMessages]{
			Log: log, Fingerprint: "v1", Form: newFakeBoard(2, 3, 4, 5, 6), Previous: prev,
		})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, keys...)
		prev = proj
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
