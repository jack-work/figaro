package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
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
	return store.VersionedPatch{Version: v, Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{key: json.RawMessage(val)}, nil)}
}

// Each entry's StudyVersions bracket the studied patches since the previous
// stamp; a stamped member with no accessor renders nothing.
func TestProjectionFoldsStudiedPatchesBetweenStamps(t *testing.T) {
	// MemLog does not persist StudyVersions.
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
	_, err := CatchUp(CatchUpConfig{
		Log:        stamped,
		Translator: store.NewMemLog[[]json.RawMessage](),
		Studies:    map[string]Form{"@r": role},
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			folded = append(folded, m)
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
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
	// An output record cannot carry a study block, so its window passes to
	// the next user record.
	if n := len(folded[1].StudyPatches["@r"]); n != 0 {
		t.Fatalf("entry2 (output) @r patches = %d, want 0: it cannot show them", n)
	}
	if n := len(folded[2].StudyPatches["@r"]); n != 2 {
		t.Fatalf("entry3 @r patches = %d, want 2 (versions 2,3, inherited from the output's window)", n)
	}
	// No accessor is unreadability, not death; death arrives as
	// system.libretto.alive.
	if _, ok := folded[2].StudyPatches["@gone"]; ok {
		t.Fatalf("@gone rendered a block with no accessor: %v", folded[2].StudyPatches["@gone"])
	}
}

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

func TestStudyReminderTextsDeterministic(t *testing.T) {
	msg := message.Message{
		Study: &message.StudyMark{FormID: "@r", Began: true},
		StudyPatches: map[string][]message.Patch{
			"@b": {form.Build(form.Snapshot{}, map[string]json.RawMessage{"x": json.RawMessage(`1`)}, nil)},
			"@a": {form.Build(form.Snapshot{}, map[string]json.RawMessage{"y": json.RawMessage(`2`)}, nil)},
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

// A window folds to one block holding the value each key ends at; the body
// is a JSON object, not prose.
func TestStudyWindowIsFoldedToItsResult(t *testing.T) {
	msg := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {
			form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"stand by"`), "doomed": json.RawMessage(`1`)}, nil),
			form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"go"`)}, nil),
			form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"the watchword is COLUMBINE"`)}, []string{"doomed"}),
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
	withVersion := msg
	withVersion.StudyAt = map[string]uint64{"@r": 7}
	if v := strings.Join(StudyReminderTexts(withVersion, form.Snapshot{}), "\n"); !strings.Contains(v, `"version":7`) {
		t.Errorf("the version must ride the block: %s", v)
	}
	if strings.Contains(joined, "figaro studies") || strings.Contains(joined, "since the last turn") {
		t.Errorf("the body must be structural: %s", joined)
	}
}

// system.* keys are not rendered.
func TestStudyRenderSkipsTheHarnessNamespace(t *testing.T) {
	msg := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.studies": json.RawMessage(`["@x"]`),
			"brief":          json.RawMessage(`"visible"`),
		}, nil)},
	}}
	joined := strings.Join(StudyReminderTexts(msg, form.Snapshot{}), "\n")
	if strings.Contains(joined, "system.studies") {
		t.Errorf("harness namespace leaked: %s", joined)
	}
	if !strings.Contains(joined, "visible") {
		t.Errorf("the readable key is missing: %s", joined)
	}
	only := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.x": json.RawMessage(`1`)}, nil)},
	}}
	if texts := StudyReminderTexts(only, form.Snapshot{}); len(texts) != 0 {
		t.Errorf("want no block, got %v", texts)
	}
}

// The began mark carries the baseline state and no second block repeats it.
func TestStudyMarkCarriesTheBaselineState(t *testing.T) {
	msg := message.Message{
		Study: &message.StudyMark{FormID: "@r", Began: true},
		StudyPatches: map[string][]message.Patch{"@r": {
			form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"stand by"`)}, nil),
			form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"warden"`)}, nil),
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

	msg.StudyPatches["@other"] = []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{"x": json.RawMessage(`1`)}, nil)}
	if got := len(StudyReminderTexts(msg, form.Snapshot{})); got != 2 {
		t.Errorf("want the mark plus one fold, got %d", got)
	}

	stop := message.Message{Study: &message.StudyMark{FormID: "@r", Began: false}}
	if s := strings.Join(StudyReminderTexts(stop, form.Snapshot{}), ""); !strings.Contains(s, `"observing":false`) || strings.Contains(s, `"state"`) {
		t.Errorf("stop mark: %s", s)
	}
}

// A window may only close on a record that can carry the block: a source
// change written during a turn lands in the window that closes on the
// turn's own answer.
func TestAssistantRecordDoesNotSwallowAStudyWindow(t *testing.T) {
	entries := []store.Entry[message.Message]{
		{LT: 1, Payload: message.Message{Role: message.RoleInput}, StudyVersions: map[string]uint64{"@r": 1}},
		{LT: 2, Payload: message.Message{Role: message.RoleOutput}, StudyVersions: map[string]uint64{"@r": 3}},
		{LT: 3, Payload: message.Message{Role: message.RoleInput}, StudyVersions: map[string]uint64{"@r": 3}},
	}
	role := &fakeForm{ps: []store.VersionedPatch{
		vp(1, "name", `"r"`), vp(2, "phase", `"canary"`), vp(3, "phase", `"ga"`),
	}}

	var folded []message.Message
	_, err := CatchUp(CatchUpConfig{
		Log:        &stampedLog{entries: entries},
		Translator: store.NewMemLog[[]json.RawMessage](),
		Studies:    map[string]Form{"@r": role},
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			folded = append(folded, m)
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(folded[1].StudyPatches["@r"]); n != 0 {
		t.Errorf("the assistant record rendered %d study patches it cannot show", n)
	}
	if n := len(folded[2].StudyPatches["@r"]); n != 2 {
		t.Fatalf("the next user record folded %d patches, want 2 (versions 2 and 3): the window was swallowed", n)
	}
}

// A death arrives as system.libretto.alive=false, the one system key
// studyFold must not drop.
func TestADeadSourceIsRenderedAsAFact(t *testing.T) {
	msg := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {form.Build(form.Snapshot{}, map[string]json.RawMessage{
			store.KeyLibrettoAlive: json.RawMessage(`false`),
			store.KeyLibrettoAt:    json.RawMessage(`9`),
		}, nil)},
	}}
	joined := strings.Join(StudyReminderTexts(msg, form.Snapshot{}), "\n")
	if !strings.Contains(joined, `"exists":false`) {
		t.Fatalf("the death was not rendered: %q", joined)
	}
	if strings.Contains(joined, "system.libretto") {
		t.Errorf("the machinery leaked with it: %q", joined)
	}

	// Bookkeeping alone renders nothing.
	quiet := message.Message{StudyPatches: map[string][]message.Patch{
		"@r": {form.Build(form.Snapshot{}, map[string]json.RawMessage{store.KeyLibrettoAt: json.RawMessage(`10`)}, nil)},
	}}
	if got := StudyReminderTexts(quiet, form.Snapshot{}); len(got) != 0 {
		t.Errorf("bookkeeping rendered a block: %v", got)
	}
}
