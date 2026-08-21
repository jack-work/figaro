package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// AN ENTRY THAT CONSUMES A STUDY WINDOW AND WRITES NO ROW LOSES THE CHANGE
// FOREVER, ON THE WRITE PATH.
//
// Gluck, 2026-08-20: a role-purpose set on a studied form never reached his
// figaro, while a later role-desc did. This is the shape that does it.
//
// The deriver advances its study cursor for any RoleInput entry, because such
// an entry CAN carry the block. But an entry can carry it and still encode to
// NOTHING -- a contentless input record, which the real store is full of --
// and then the store writes no row. The cursor has moved past the window and
// the bytes that would have shown it were never written.
//
// The catch-up survives this because it re-seeds from the last WRITTEN row
// every pass. The write path holds its cursor IN MEMORY for the life of the
// daemon, so for it the window is simply gone.
//
// FIXED (Gluck's design): a cursor advances only when a row is WRITTEN. The
// deriver computes in Next and moves in Commit, and both callers commit
// exactly when the store accepted a row -- so an entry that encodes to nothing
// leaves its window open and the delta rides the next entry that does not.
//
// This test asserted the DEFECT until the fix landed, and its failure message
// told its author to invert it, which is what happened.
func TestAContentlessEntryDoesNotEatTheStudyWindow(t *testing.T) {
	const fid = "@studied"

	// The studied form gained one patch, at libretto version 7.
	//
	// A STATELESS ACCESSOR, deliberately. newFakeBoard's cursor CONSUMES: it
	// yields each version once and never again, so a test using it cannot tell
	// "the deriver never asked" from "the fake had nothing left" -- and this
	// test's whole subject is whether the range is asked for twice.
	studies := map[string]Form{fid: patchAt(7)}

	log := store.NewMemLog[message.Message]()
	// 1: a CONTENTLESS input record, stamped with the new study version. It
	//    can carry a block and encodes to nothing.
	if _, err := log.Append(store.Entry[message.Message]{
		StudyVersions: map[string]uint64{fid: 7},
		Payload:       message.Message{Role: message.RoleInput},
	}); err != nil {
		t.Fatal(err)
	}
	// 2: the next real prompt, at the same study version.
	if _, err := log.Append(store.Entry[message.Message]{
		StudyVersions: map[string]uint64{fid: 7},
		Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// The encoder writes nothing for a contentless record, exactly as a real
	// one does, and reports the study keys it was handed for the rest.
	var sawStudyOn []string
	encode := func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
		if len(msg.Content) == 0 {
			return nil, nil
		}
		for fid := range msg.StudyPatches {
			sawStudyOn = append(sawStudyOn, fid)
		}
		body, _ := json.Marshal(map[string]any{"text": msg.Content[0].Text})
		return []json.RawMessage{body}, nil
	}

	trans := store.NewMemLog[[]json.RawMessage]()
	adapter := NewOnAppend("fake", encode, func() string { return "fp/v1" },
		func(string) Form { return nil },
		func(string) map[string]Form { return studies },
		func(string) (store.Log[[]json.RawMessage], error) { return trans, nil },
	)

	for _, e := range log.Read() {
		encoded, fp, err := adapter.EncodeEntry("aria", log, e)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) == 0 {
			continue // the store writes nothing, and neither do we
		}
		if _, err := trans.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: e.LT, Payload: encoded, Fingerprint: fp,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if len(sawStudyOn) == 0 {
		t.Fatal("THE STUDY CHANGE WAS LOST: the contentless record consumed the window " +
			"at version 7 and wrote no row, so the prompt that followed carried nothing. " +
			"This is the shape of Gluck's vanished role-purpose, and the cure is that a " +
			"cursor advances only when a row is written.")
	}
}

// patchAt answers PatchesBetween honestly and repeatedly: one patch at the
// given version, for any range that contains it.
type patchAt uint64

func (v patchAt) PatchesBetween(after, upTo uint64) []message.Patch {
	if uint64(v) <= after || uint64(v) > upTo {
		return nil
	}
	return []message.Patch{{Set: map[string]json.RawMessage{"role-purpose": json.RawMessage(`"carry the razor"`)}}}
}

// GLUCK'S OWN EXAMPLE, 2026-08-20, as the specification:
//
//	translated   fig IR message, cursor at 6
//	untranslated fig IR message, cursor at 7
//	untranslated fig IR message, cursor at 15   (many patches in between)
//	translated   fig IR message, cursor at 15
//
//	"translator should look back in figaro ir, see 15, but know that it was an
//	 untranslated message, so it needs to look further ... then it sees the
//	 cursor at 6, the fig IR message is translatable, and so it gets a cursor
//	 delta of 9, and folds the patch from libretto LTs 6-15"
//
// THE FIG IR STAMP ALWAYS ADVANCES -- it is written at append time and is a
// fact about the record. What must not advance is the TRANSLATOR's notion of
// what the model has actually been shown, and walking the log forward that is
// simply "commit on write": the range stays open across every entry that
// produced no row, so the next one that does gets (6, 15].
func TestTheDeltaSpansEveryUntranslatedMessageBetween(t *testing.T) {
	const fid = "@studied"
	studies := map[string]Form{fid: patchesEvery{}}

	entry := func(stamp uint64, body string) store.Entry[message.Message] {
		m := message.Message{Role: message.RoleInput}
		if body != "" {
			m.Content = []message.Content{message.TextContent(body)}
		}
		return store.Entry[message.Message]{StudyVersions: map[string]uint64{fid: stamp}, Payload: m}
	}

	d := NewDeriver(nil, studies)
	// The first translated message sets where the model's knowledge stands.
	msg, _, ok := d.Next(entry(6, "seen"))
	if !ok {
		t.Fatal("the opening message must be translatable")
	}
	d.Commit(entry(6, "seen"), msg)

	// Two records land that translate to nothing. Their stamps advance in the
	// fig IR; the translator's cursor must not.
	for _, at := range []uint64{7, 15} {
		e := entry(at, "")
		m, _, _ := d.Next(e)
		if len(m.Content) != 0 {
			t.Fatalf("fixture: the record at %d should encode to nothing", at)
		}
		// No row is written, so no Commit -- exactly what both callers do.
	}

	// The next message that DOES translate must carry the whole span.
	m, _, ok := d.Next(entry(15, "next real prompt"))
	if !ok {
		t.Fatal("the following prompt must be translatable")
	}
	got := m.StudyPatches[fid]
	if len(got) != 9 {
		t.Fatalf("the delta spans %d patches, want 9 -- libretto 6 to 15, across "+
			"both untranslated messages. Gluck's example is the specification.", len(got))
	}
	if at := m.StudyAt[fid]; at != 15 {
		t.Fatalf("the block names libretto version %d, want 15", at)
	}
}

// patchesEvery yields one patch per version in (after, upTo], statelessly.
type patchesEvery struct{}

func (patchesEvery) PatchesBetween(after, upTo uint64) []message.Patch {
	var out []message.Patch
	for v := after + 1; v <= upTo; v++ {
		out = append(out, message.Patch{Set: map[string]json.RawMessage{
			"k": json.RawMessage(`"v"`),
		}})
	}
	return out
}

// THE TRANSLATOR ASSUMES THE STORE WRITES WHAT IT HANDS BACK, and this is what
// happens when that assumption breaks.
//
// Gluck's constraint: the fig IR side must not know which blocks were
// translated, so nothing reports a write back to it. The cursor therefore
// advances when the translator RETURNS bytes, not when they land -- and a
// store-side append failure leaves the in-memory cursor ahead of the log.
//
// It is not lost, because the catch-up derives its watermark from the last
// WRITTEN row and re-walks everything after it. That is the self-healing half
// of the two-path design, and it is asserted here rather than believed.
func TestACatchUpRecoversAWindowAWriteFailureSkipped(t *testing.T) {
	const fid = "@studied"
	studies := map[string]Form{fid: patchesEvery{}}

	log := store.NewMemLog[message.Message]()
	for _, at := range []uint64{6, 15} {
		if _, err := log.Append(store.Entry[message.Message]{
			StudyVersions: map[string]uint64{fid: at},
			Payload: message.Message{
				Role: message.RoleInput, Content: []message.Content{message.TextContent("m")},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The write path handled BOTH entries and its cursor is at 15 -- but the
	// store accepted NO rows, as a failing append would leave it.
	adapter := NewOnAppend("fake",
		func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			return []json.RawMessage{json.RawMessage(`{"role":"user"}`)}, nil
		},
		func() string { return "fp/v1" },
		func(string) Form { return nil },
		func(string) map[string]Form { return studies },
		func(string) (store.Log[[]json.RawMessage], error) { return store.NewMemLog[[]json.RawMessage](), nil },
	)
	for _, e := range log.Read() {
		if _, _, err := adapter.EncodeEntry("aria", log, e); err != nil {
			t.Fatal(err)
		}
	}

	// The next send catches up. Its watermark is the last WRITTEN row -- there
	// is none -- so it must render the whole span, not trust anyone's cursor.
	var seen int
	trans := store.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(CatchUpConfig{
		Log: log, Translator: trans, Studies: studies, Fingerprint: "fp/v1",
		Encode: func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			seen += len(msg.StudyPatches[fid])
			return []json.RawMessage{json.RawMessage(`{"role":"user"}`)}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 15 {
		t.Fatalf("the catch-up rendered %d patches, want 15: a write failure must not "+
			"cost a window, because the watermark is the last WRITTEN row and not a "+
			"cursor anybody kept", seen)
	}
}
