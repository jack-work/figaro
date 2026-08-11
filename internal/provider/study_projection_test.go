package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

type fakeForm struct{ ps []store.VersionedPatch }

func (f *fakeForm) PatchesBetween(after, upTo uint64) []message.Patch {
	var out []message.Patch
	for _, p := range f.ps {
		if p.Version > after && p.Version <= upTo {
			out = append(out, p.Patch)
		}
	}
	return out
}

func vp(v uint64, key, val string) store.VersionedPatch {
	return store.VersionedPatch{Version: v, Patch: message.Patch{Set: map[string]json.RawMessage{key: json.RawMessage(val)}}}
}

// The observed set folds at the stamps: each entry's StudyVersions
// bracket exactly the studied patches since the previous stamp — the
// bound board's derivation, generalized — and a stamped member with no
// accessor renders a tombstone note instead of silence.
func TestProjectionFoldsStudiedPatchesBetweenStamps(t *testing.T) {
	// MemLog does not persist StudyVersions — drive the projection with a
	// wrapper that stamps entries the way the xwal log does.
	entries := []store.Entry[message.Message]{
		{LT: 1, Payload: message.Message{Role: message.RoleInput}, StudyVersions: map[string]uint64{"@r": 1}},
		{LT: 2, Payload: message.Message{Role: message.RoleOutput}, StudyVersions: map[string]uint64{"@r": 3}},
		{LT: 3, Payload: message.Message{Role: message.RoleInput}, StudyVersions: map[string]uint64{"@r": 3, "@gone": 9}},
	}
	stamped := &stampedLog{entries: entries}

	role := &fakeForm{ps: []store.VersionedPatch{
		vp(1, "name", `"r"`), vp(2, "phase", `"canary"`), vp(3, "phase", `"ga"`),
	}}

	var folded []message.Message
	_, _, err := ProjectIncrementally(ProjectionConfig[int]{
		Log:     stamped,
		Studies: map[string]Form{"@r": role},
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			folded = append(folded, m)
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
		Append: func(s int, _ []json.RawMessage, _ uint64) int { return s + 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(folded) != 3 {
		t.Fatalf("folded %d messages", len(folded))
	}
	if n := len(folded[0].StudyPatches["@r"]); n != 1 {
		t.Fatalf("entry1 @r patches = %d, want 1 (version 1 only)", n)
	}
	if n := len(folded[1].StudyPatches["@r"]); n != 2 {
		t.Fatalf("entry2 @r patches = %d, want 2 (versions 2,3)", n)
	}
	if len(folded[2].StudyPatches["@r"]) != 0 {
		t.Fatalf("entry3 @r patches = %v, want none (stamp unmoved)", folded[2].StudyPatches["@r"])
	}
	if note := folded[2].StudyNotes["@gone"]; !strings.Contains(note, "no longer exists") {
		t.Fatalf("tombstone missing for @gone: %q", note)
	}
}

// stampedLog serves a fixed entry slice through the store.Log surface
// (unused interface corners panic loudly via the embedded nil).
type stampedLog struct {
	store.Log[message.Message]
	entries []store.Entry[message.Message]
}

func (l *stampedLog) Read() []store.Entry[message.Message] { return l.entries }
func (l *stampedLog) Len() int                             { return len(l.entries) }
func (l *stampedLog) ReadFrom(from uint64, limit int) []store.Entry[message.Message] {
	var out []store.Entry[message.Message]
	for _, e := range l.entries {
		if e.FigaroLT >= from || e.LT >= from {
			out = append(out, e)
		}
	}
	return out
}

// The render is deterministic and provider-neutral: marks, folds
// (members sorted), tombstones.
func TestStudyReminderTextsDeterministic(t *testing.T) {
	msg := message.Message{
		Study: &message.StudyMark{FormID: "@r", Began: true},
		StudyPatches: map[string][]message.Patch{
			"@b": {{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}},
			"@a": {{Set: map[string]json.RawMessage{"y": json.RawMessage(`2`)}}},
		},
		StudyNotes: map[string]string{"@gone": "the observed form no longer exists (removed while studied)"},
	}
	a := StudyReminderTexts(msg)
	b := StudyReminderTexts(msg)
	if strings.Join(a, "|") != strings.Join(b, "|") {
		t.Fatal("render not deterministic")
	}
	joined := strings.Join(a, "\n")
	if !strings.Contains(joined, "began observing form @r") {
		t.Errorf("mark missing: %s", joined)
	}
	if strings.Index(joined, "study:@a") > strings.Index(joined, "study:@b") {
		t.Errorf("members not sorted: %s", joined)
	}
	if !strings.Contains(joined, "no longer exists") {
		t.Errorf("tombstone missing")
	}
}
