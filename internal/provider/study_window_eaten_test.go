package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
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
