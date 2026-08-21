package provider_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

func incantBoard(t *testing.T, key, raw string) form.Snapshot {
	t.Helper()
	return form.FromMap(map[string]json.RawMessage{key: json.RawMessage(raw)})
}

// The three study events each carry their own phrase, and only their own.
func TestStudyIncantationRidesEachEvent(t *testing.T) {
	board := incantBoard(t, form.StudyIncantationKey,
		`{"onstudy":"BEGIN","onupdate":"MOVED","ondrop":"END"}`)

	began := message.Message{Study: &message.StudyMark{FormID: "@f", Began: true}}
	if s := strings.Join(provider.StudyReminderTexts(began, board), ""); !strings.Contains(s, `"say":"BEGIN"`) {
		t.Fatalf("onstudy must ride the begin mark: %s", s)
	} else if strings.Contains(s, "MOVED") || strings.Contains(s, "END") {
		t.Fatalf("only the event's own phrase may appear: %s", s)
	}

	moved := message.Message{
		StudyPatches: map[string][]message.Patch{"@f": {{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}}}},
		StudyAt:      map[string]uint64{"@f": 3},
	}
	if s := strings.Join(provider.StudyReminderTexts(moved, board), ""); !strings.Contains(s, `"say":"MOVED"`) {
		t.Fatalf("onupdate must ride a fold block: %s", s)
	}

	dropped := message.Message{Study: &message.StudyMark{FormID: "@f", Began: false}}
	if s := strings.Join(provider.StudyReminderTexts(dropped, board), ""); !strings.Contains(s, `"say":"END"`) {
		t.Fatalf("ondrop must ride the stop mark: %s", s)
	}
}

// No incantation, no change: the blocks are byte-identical to what they were
// before the feature existed. The per-LT translation cache makes any drift
// here permanent, so this is the compatibility guard.
func TestStudyBlocksUnchangedWithoutIncantation(t *testing.T) {
	msg := message.Message{
		Study:        &message.StudyMark{FormID: "@f", Began: true},
		StudyPatches: map[string][]message.Patch{"@f": {{Set: map[string]json.RawMessage{"k": json.RawMessage(`"v"`)}}}},
		StudyAt:      map[string]uint64{"@f": 3},
	}
	bare := strings.Join(provider.StudyReminderTexts(msg, form.Snapshot{}), "\n")
	other := strings.Join(provider.StudyReminderTexts(msg,
		incantBoard(t, "mantra", `"unrelated"`)), "\n")
	if bare != other {
		t.Fatalf("an unrelated board changed the rendering:\n%s\n---\n%s", bare, other)
	}
	if strings.Contains(bare, `"say"`) {
		t.Fatalf("no incantation must mean no say field: %s", bare)
	}
}

// A malformed incantation costs its phrase and nothing else: the fact still
// reaches the model.
func TestStudyBlockSurvivesMalformedIncantation(t *testing.T) {
	board := incantBoard(t, form.StudyIncantationKey, `["nope"]`)
	msg := message.Message{Study: &message.StudyMark{FormID: "@f", Began: true}}
	s := strings.Join(provider.StudyReminderTexts(msg, board), "")
	if !strings.Contains(s, `"observing":true`) {
		t.Fatalf("the study fact must survive a bad incantation: %s", s)
	}
	if strings.Contains(s, `"say"`) {
		t.Fatalf("a refused incantation must not render: %s", s)
	}
}

func TestForkIncantationRendersOnlyOnTheBirthPatch(t *testing.T) {
	board := incantBoard(t, form.ForkIncantationKey, `"you are a branch"`)
	born := message.Message{Patches: []message.Patch{{Set: map[string]json.RawMessage{
		form.ForkedFromKey: json.RawMessage(`"abc123"`),
	}}}}
	s := strings.Join(provider.ForkReminderTexts(born, board), "")
	if !strings.Contains(s, `"say":"you are a branch"`) || !strings.Contains(s, `"forked_from":"abc123"`) {
		t.Fatalf("the fork block must carry the phrase and the parent: %s", s)
	}

	ordinary := message.Message{Patches: []message.Patch{{Set: map[string]json.RawMessage{
		"mantra": json.RawMessage(`"hello"`),
	}}}}
	if got := provider.ForkReminderTexts(ordinary, board); len(got) != 0 {
		t.Fatalf("an ordinary patch is not a fork: %v", got)
	}
}

// The default is silence, and it stays silence. Forked arias have never been
// told they were forked; turning that on for everyone who never asked would
// put a new block in every branch in the store.
func TestForkSilentWithoutIncantation(t *testing.T) {
	born := message.Message{Patches: []message.Patch{{Set: map[string]json.RawMessage{
		form.ForkedFromKey: json.RawMessage(`"abc123"`),
	}}}}
	if got := provider.ForkReminderTexts(born, form.Snapshot{}); len(got) != 0 {
		t.Fatalf("no incantation must render nothing, got %v", got)
	}
}
