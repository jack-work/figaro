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
// bracket exactly the studied patches since the previous stamp: the
// bound board's derivation, generalized, and a stamped member with no
// accessor renders nothing at all.
func TestProjectionFoldsStudiedPatchesBetweenStamps(t *testing.T) {
	// MemLog does not persist StudyVersions: drive the projection with a
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
	// entry2 is an OUTPUT record. It cannot carry a study block (every
	// encoder renders them under RoleInput), so it neither folds nor
	// consumes: its window passes to the next user record instead of being
	// computed and dropped. This assertion used to read the other way, and
	// what it was pinning was a silent loss.
	if n := len(folded[1].StudyPatches["@r"]); n != 0 {
		t.Fatalf("entry2 (output) @r patches = %d, want 0: it cannot show them", n)
	}
	if n := len(folded[2].StudyPatches["@r"]); n != 2 {
		t.Fatalf("entry3 @r patches = %d, want 2 (versions 2,3, inherited from the output's window)", n)
	}
	// @gone is stamped with no accessor: a libretto is fully persistent, so
	// this is unreadability, not death, and absence is the truthful answer.
	// A dead SOURCE arrives as system.libretto.alive on the libretto itself.
	if _, ok := folded[2].StudyPatches["@gone"]; ok {
		t.Fatalf("@gone rendered a block with no accessor: %v", folded[2].StudyPatches["@gone"])
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
// (members sorted).
func TestStudyReminderTextsDeterministic(t *testing.T) {
	msg := message.Message{
		Study: &message.StudyMark{FormID: "@r", Began: true},
		StudyPatches: map[string][]message.Patch{
			"@b": {{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}},
			"@a": {{Set: map[string]json.RawMessage{"y": json.RawMessage(`2`)}}},
		},
	}
	a := StudyReminderTexts(msg, form.Snapshot{})
	b := StudyReminderTexts(msg, form.Snapshot{})
	if strings.Join(a, "|") != strings.Join(b, "|") {
		t.Fatal("render not deterministic")
	}
	joined := strings.Join(a, "\n")
	if !strings.Contains(joined, `"form":"@r","observing":true`) {
		t.Errorf("mark missing: %s", joined)
	}
	if strings.Index(joined, "study:@a") > strings.Index(joined, "study:@b") {
		t.Errorf("members not sorted: %s", joined)
	}
}

// The window is folded, not narrated. Three patches to one key inside one
// window render ONE block holding the value the key ENDS at, because a model
// shown the intermediate values answers from them. A haiku did exactly that in
// the fifty-observer storm: asked what the brief said, it reported the value
// the brief held before the change it was being asked about.
//
// The body is STRUCTURE, not prose (Gluck's rule): one compact JSON object
// naming the form and what moved. The skills contextualize it.
func TestStudyWindowIsFoldedToItsResult(t *testing.T) {
	msg := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {
			{Set: map[string]json.RawMessage{"brief": json.RawMessage(`"stand by"`), "doomed": json.RawMessage(`1`)}},
			{Set: map[string]json.RawMessage{"brief": json.RawMessage(`"go"`)}},
			{Set: map[string]json.RawMessage{"brief": json.RawMessage(`"the watchword is COLUMBINE"`)}, Remove: []string{"doomed"}},
		},
	}}
	joined := strings.Join(StudyReminderTexts(msg, form.Snapshot{}), "\n")
	if n := strings.Count(joined, `system-reminder name="study:@r"`); n != 1 {
		t.Fatalf("want ONE block for one form, got %d: %s", n, joined)
	}
	if !strings.Contains(joined, "COLUMBINE") {
		t.Errorf("the current value is missing: %s", joined)
	}
	if strings.Contains(joined, "stand by") || strings.Contains(joined, `"go"`) {
		t.Errorf("an intermediate value survived the fold: %s", joined)
	}
	if !strings.Contains(joined, `"changes":3`) {
		t.Errorf("the number of changes is worth stating: %s", joined)
	}
	if !strings.Contains(joined, `"removed":["doomed"]`) {
		t.Errorf("a removal must be stated: %s", joined)
	}
	if !strings.Contains(joined, `"form":"@r"`) {
		t.Errorf("the block must name its form: %s", joined)
	}
	// A version is rendered when the projection supplies one, so two blocks
	// about one form can be ordered by a reader.
	withVersion := msg
	withVersion.StudyAt = map[string]uint64{"@r": 7}
	if v := strings.Join(StudyReminderTexts(withVersion, form.Snapshot{}), "\n"); !strings.Contains(v, `"version":7`) {
		t.Errorf("the version must ride the block: %s", v)
	}
	// Structure, not prose: no sentences in the body.
	if strings.Contains(joined, "figaro studies") || strings.Contains(joined, "since the last turn") {
		t.Errorf("the body must be structural: %s", joined)
	}
}

// system.* belongs to the machinery, not to a reader: the board's own
// renderer skips it, and an observed form is not different.
func TestStudyRenderSkipsTheHarnessNamespace(t *testing.T) {
	msg := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {{Set: map[string]json.RawMessage{
			"system.studies": json.RawMessage(`["@x"]`),
			"brief":          json.RawMessage(`"visible"`),
		}}},
	}}
	joined := strings.Join(StudyReminderTexts(msg, form.Snapshot{}), "\n")
	if strings.Contains(joined, "system.studies") {
		t.Errorf("harness namespace leaked: %s", joined)
	}
	if !strings.Contains(joined, "visible") {
		t.Errorf("the readable key is missing: %s", joined)
	}
	// A window of nothing BUT system keys renders no block at all.
	only := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {{Set: map[string]json.RawMessage{"system.x": json.RawMessage(`1`)}}},
	}}
	if texts := StudyReminderTexts(only, form.Snapshot{}); len(texts) != 0 {
		t.Errorf("want no block, got %v", texts)
	}
}

// When observation BEGINS, the window is the form's whole history, which folds
// to its STATE rather than to a change. It rides the mark, labelled as state,
// and no second block repeats it: two structurally identical blocks differing
// only in a version number is what a small model reads backwards.
func TestStudyMarkCarriesTheBaselineState(t *testing.T) {
	msg := message.Message{
		Study: &message.StudyMark{FormID: "@r", Began: true},
		StudyPatches: map[string][]message.Patch{"@r": {
			{Set: map[string]json.RawMessage{"brief": json.RawMessage(`"stand by"`)}},
			{Set: map[string]json.RawMessage{"name": json.RawMessage(`"warden"`)}},
		}},
		StudyAt: map[string]uint64{"@r": 2},
	}
	texts := StudyReminderTexts(msg, form.Snapshot{})
	if len(texts) != 1 {
		t.Fatalf("want ONE block at the mark, got %d: %v", len(texts), texts)
	}
	joined := texts[0]
	for _, want := range []string{`"observing":true`, `"state":`, `"stand by"`, `"warden"`, `"version":2`} {
		if !strings.Contains(joined, want) {
			t.Errorf("mark block missing %s: %s", want, joined)
		}
	}
	if strings.Contains(joined, `"changes"`) {
		t.Errorf("a baseline is not a change count: %s", joined)
	}

	// A form that is NOT the one being marked still renders its own block.
	msg.StudyPatches["@other"] = []message.Patch{{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}}
	if got := len(StudyReminderTexts(msg, form.Snapshot{})); got != 2 {
		t.Errorf("want the mark plus one fold, got %d", got)
	}

	// Stopping observation says so and carries no state.
	stop := message.Message{Study: &message.StudyMark{FormID: "@r", Began: false}}
	if s := strings.Join(StudyReminderTexts(stop, form.Snapshot{}), ""); !strings.Contains(s, `"observing":false`) || strings.Contains(s, `"state"`) {
		t.Errorf("stop mark: %s", s)
	}
}

// A WINDOW MAY ONLY CLOSE ON A RECORD THAT CAN CARRY THE BLOCK.
//
// Study transitions ride a USER message (every encoder renders them under
// RoleInput). If an assistant record consumes its window, the block it
// computes is dropped and the next user record asks for (v, v] -- so a
// studied form's change is never shown, to anyone, ever.
//
// With a libretto this is the COMMON case, not a corner: the fold is
// asynchronous, so a source change written during a turn lands in the window
// that closes on the turn's own answer.
func TestAssistantRecordDoesNotSwallowAStudyWindow(t *testing.T) {
	entries := []store.Entry[message.Message]{
		{LT: 1, Payload: message.Message{Role: message.RoleInput}, StudyVersions: map[string]uint64{"@r": 1}},
		// The source moved DURING the turn: the answer stamps past it.
		{LT: 2, Payload: message.Message{Role: message.RoleOutput}, StudyVersions: map[string]uint64{"@r": 3}},
		{LT: 3, Payload: message.Message{Role: message.RoleInput}, StudyVersions: map[string]uint64{"@r": 3}},
	}
	role := &fakeForm{ps: []store.VersionedPatch{
		vp(1, "name", `"r"`), vp(2, "phase", `"canary"`), vp(3, "phase", `"ga"`),
	}}

	var folded []message.Message
	_, _, err := ProjectIncrementally(ProjectionConfig[int]{
		Log:     &stampedLog{entries: entries},
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
	if n := len(folded[1].StudyPatches["@r"]); n != 0 {
		t.Errorf("the assistant record rendered %d study patches it cannot show", n)
	}
	// The user record that FOLLOWS it must see everything since version 1.
	if n := len(folded[2].StudyPatches["@r"]); n != 2 {
		t.Fatalf("the next user record folded %d patches, want 2 (versions 2 and 3): the window was swallowed", n)
	}
}
