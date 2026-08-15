package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
)

func boardWithLimits(keyBytes, total int) form.Snapshot {
	s := form.Snapshot{}
	return s.Apply(message.Patch{Set: map[string]json.RawMessage{
		form.DeltaKeyBytesKey: json.RawMessage(itoa(keyBytes)),
		form.DeltaBytesKey:    json.RawMessage(itoa(total)),
	}})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// A fat value is cut at the per-key limit, stays valid JSON, and never
// splits a rune. A provider rejects invalid UTF-8 outright, so a
// byte-exact cut is a 400 waiting to happen.
func TestStudyBlockCutsFatValuesOnRuneBoundaries(t *testing.T) {
	fat := strings.Repeat("é", 400) // 800 bytes, two per rune
	msg := message.Message{
		Role: message.RoleInput,
		StudyPatches: map[string][]message.Patch{
			"@f1": {{Set: map[string]json.RawMessage{"blob": mustJSON(fat)}}},
		},
		StudyAt: map[string]uint64{"@f1": 7},
	}
	texts := StudyReminderTexts(msg, boardWithLimits(101, 0))
	if len(texts) != 1 {
		t.Fatalf("want one block, got %d", len(texts))
	}
	if !utf8Valid(texts[0]) {
		t.Fatal("the block is not valid UTF-8: a rune was split")
	}
	if !strings.Contains(texts[0], "…") {
		t.Fatalf("a cut value must be marked: %s", texts[0])
	}
	body := blockJSON(t, texts[0])
	set, _ := body["set"].(map[string]any)
	got, _ := set["blob"].(string)
	if len(got) > 140 {
		t.Fatalf("value not bounded: %d bytes", len(got))
	}
}

// Past the TOTAL budget, keys are elided and the reader is told where
// the rest lives -- executable advice, not a shrug.
func TestStudyBlockElidesPastTheTotalAndSaysWhere(t *testing.T) {
	set := map[string]json.RawMessage{}
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		set[k] = mustJSON(strings.Repeat(k, 200))
	}
	msg := message.Message{
		Role:         message.RoleInput,
		StudyPatches: map[string][]message.Patch{"@f1": {{Set: set}}},
		StudyAt:      map[string]uint64{"@f1": 3},
	}
	texts := StudyReminderTexts(msg, boardWithLimits(0, 450))
	body := blockJSON(t, texts[0])
	shown, _ := body["set"].(map[string]any)
	if len(shown) == 5 || len(shown) == 0 {
		t.Fatalf("expected a partial set, got %d of 5", len(shown))
	}
	note, _ := body["elided"].(string)
	if !strings.Contains(note, "more delta") || !strings.Contains(note, "fig form @f1 show") {
		t.Fatalf("the elision must name the verb that shows the rest: %q", note)
	}
	// Deterministic: keys are spent in sorted order, so the SAME keys
	// survive every render. The per-LT cache makes the first rendering
	// permanent; a shuffled fold would make history disagree with itself.
	for i := 0; i < 5; i++ {
		again := blockJSON(t, StudyReminderTexts(msg, boardWithLimits(0, 450))[0])
		if !sameKeys(shown, again["set"].(map[string]any)) {
			t.Fatal("two renderings of one record disagree: the fold is not deterministic")
		}
	}
}

// A negative limit is unbounded: what figaro did before limits existed.
func TestStudyBlockNegativeLimitIsUnbounded(t *testing.T) {
	fat := strings.Repeat("x", 5000)
	msg := message.Message{
		Role: message.RoleInput,
		StudyPatches: map[string][]message.Patch{
			"@f1": {{Set: map[string]json.RawMessage{"blob": mustJSON(fat)}}},
		},
		StudyAt: map[string]uint64{"@f1": 1},
	}
	texts := StudyReminderTexts(msg, boardWithLimits(-1, -1))
	if !strings.Contains(texts[0], fat) {
		t.Fatal("a negative limit must render the value whole")
	}
}

func mustJSON(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func blockJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	i := strings.Index(text, "{")
	j := strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		t.Fatalf("no JSON body in block: %s", text)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text[i:j+1]), &m); err != nil {
		t.Fatalf("block is not parseable JSON (%v): %s", err, text[i:j+1])
	}
	return m
}

func sameKeys(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}
