package provider_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

// The incantation is read from the board on the study path, which runs per
// encoded message per translate. These say what that lookup costs in the three
// shapes that matter: no key at all (the overwhelming majority of arias), the
// key present, and a message with no study event (which must not look at the
// board at all).

func benchStudyMessage() message.Message {
	return message.Message{
		Study: &message.StudyMark{FormID: "@f", Began: true},
		StudyPatches: map[string][]message.Patch{"@f": {{Set: map[string]json.RawMessage{
			"brief": json.RawMessage(`"ship it"`),
		}}}},
		StudyAt: map[string]uint64{"@f": 3},
	}
}

// A board with realistic weight: an incantation lookup is a tree lookup, and
// a one-key board would flatter it.
func benchBoard(keys int, extra map[string]json.RawMessage) form.Snapshot {
	m := make(map[string]json.RawMessage, keys+len(extra))
	for i := 0; i < keys; i++ {
		m[fmt.Sprintf("key%03d", i)] = json.RawMessage(`"value"`)
	}
	for k, v := range extra {
		m[k] = v
	}
	return form.FromMap(m)
}

func BenchmarkStudyReminderNoIncantation(b *testing.B) {
	msg := benchStudyMessage()
	board := benchBoard(40, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.StudyReminderTexts(msg, board)
	}
}

func BenchmarkStudyReminderWithIncantation(b *testing.B) {
	msg := benchStudyMessage()
	board := benchBoard(40, map[string]json.RawMessage{
		form.StudyIncantationKey: json.RawMessage(`{"onstudy":"watch","onupdate":"moved","ondrop":"away"}`),
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.StudyReminderTexts(msg, board)
	}
}

// The path every non-studying aria takes on every message: no study event, so
// the board must not be consulted at all.
func BenchmarkStudyReminderNoStudyEvent(b *testing.B) {
	msg := message.Message{Role: message.RoleInput}
	board := benchBoard(40, map[string]json.RawMessage{
		form.StudyIncantationKey: json.RawMessage(`{"onstudy":"watch"}`),
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.StudyReminderTexts(msg, board)
	}
}

func BenchmarkForkReminderOrdinaryMessage(b *testing.B) {
	msg := message.Message{Patches: []message.Patch{{Set: map[string]json.RawMessage{
		"mantra": json.RawMessage(`"hello"`),
	}}}}
	board := benchBoard(40, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.ForkReminderTexts(msg, board)
	}
}

// The control: the same render against an EMPTY board, which is what this
// function did before incantations existed. The gap between this and
// NoIncantation is the whole price paid by every aria that never sets the key.
func BenchmarkStudyReminderEmptyBoard(b *testing.B) {
	msg := benchStudyMessage()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.StudyReminderTexts(msg, form.Snapshot{})
	}
}
