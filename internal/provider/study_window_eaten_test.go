package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// A contentless input record can carry a study block and encodes to
// nothing; its window must ride the next entry that writes a row.
func TestAContentlessEntryDoesNotEatTheStudyWindow(t *testing.T) {
	const fid = "@studied"

	// A stateless accessor: newFakeBoundForm's cursor consumes, and this test's
	// subject is whether the range is asked for twice.
	studies := map[string]Form{fid: patchAt(7)}

	log := store.NewMemLog[message.Message]()
	// 1: a contentless input record stamped with the new study version.
	if _, err := log.Append(store.Entry[message.Message]{
		StudyVersions: map[string]uint64{fid: 7},
		Payload:       message.Message{Role: message.RoleInput},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(store.Entry[message.Message]{
		StudyVersions: map[string]uint64{fid: 7},
		Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")},
		},
	}); err != nil {
		t.Fatal(err)
	}

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
		if fp == "" {
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
			"A cursor advances only when a row is written.")
	}
}

// patchAt is stateless: one patch at version, for any range containing it.
type patchAt uint64

func (v patchAt) PatchesBetween(after, upTo uint64) []message.Patch {
	if uint64(v) <= after || uint64(v) > upTo {
		return nil
	}
	return []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{"role-purpose": json.RawMessage(`"carry the razor"`)}, nil)}
}

// Stamps 6, 7, 15, 15 where the middle two translate to nothing: the fig IR
// stamp always advances, the translator's cursor only on a written row, so
// the last message folds (6, 15].
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
	msg, _, ok := d.Next(entry(6, "seen"))
	if !ok {
		t.Fatal("the opening message must be translatable")
	}
	d.Commit(entry(6, "seen"), msg)

	for _, at := range []uint64{7, 15} {
		e := entry(at, "")
		m, _, _ := d.Next(e)
		if len(m.Content) != 0 {
			t.Fatalf("fixture: the record at %d should encode to nothing", at)
		}
	}

	m, _, ok := d.Next(entry(15, "next real prompt"))
	if !ok {
		t.Fatal("the following prompt must be translatable")
	}
	got := m.StudyPatches[fid]
	if len(got) != 9 {
		t.Fatalf("the delta spans %d patches, want 9 -- libretto 6 to 15, across "+
			"both untranslated messages.", len(got))
	}
	if at := m.StudyAt[fid]; at != 15 {
		t.Fatalf("the block names libretto version %d, want 15", at)
	}
}

type patchesEvery struct{}

func (patchesEvery) PatchesBetween(after, upTo uint64) []message.Patch {
	var out []message.Patch
	for v := after + 1; v <= upTo; v++ {
		out = append(out, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"k": json.RawMessage(`"v"`),
		}, nil))
	}
	return out
}

// The write path's cursor advances when bytes are returned, not when they
// land; a failed append leaves it ahead of the log, and the next catch-up
// re-walks from the last written row.
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
