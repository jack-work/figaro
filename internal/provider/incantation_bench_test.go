package provider_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

func benchStudyMessage() message.Message {
	return message.Message{
		Study: &message.StudyMark{FormID: "@f", Began: true},
		StudyPatches: map[string][]message.Patch{"@f": {form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"brief": json.RawMessage(`"ship it"`),
		}, nil)}},
		StudyAt: map[string]uint64{"@f": 3},
	}
}

// A one-key board would flatter the tree lookup.
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
	msg := message.Message{Patches: []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"mantra": json.RawMessage(`"hello"`),
	}, nil)}}
	board := benchBoard(40, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.ForkReminderTexts(msg, board)
	}
}

// The control: an empty board.
func BenchmarkStudyReminderEmptyBoard(b *testing.B) {
	msg := benchStudyMessage()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provider.StudyReminderTexts(msg, form.Snapshot{})
	}
}
