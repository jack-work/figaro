package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// A DEFERRED PATCH MUST SURVIVE THE PROCESS THAT DEFERRED IT.
//
// Holding a patch back until a person speaks again means the board version a
// row consumed is no longer "the version stamped on the record it translates".
// A tool result writes a row and consumes nothing; the next question writes a
// row and consumes everything pending.
//
// So the row says what it consumed, and a cold start reads that. Seeding from
// the IR record at the watermark instead -- which is what a cursor did before
// the window could stay open -- reads the tool result's stamp, concludes the
// patch was rendered, and the change reaches nobody. The daemon restarts more
// often than Gluck changes a mantra, so this is not a corner.
func TestADeferredPatchSurvivesAColdStart(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	rows := store.NewMemLog[[]json.RawMessage]()
	add := func(msg message.Message, board uint64) {
		if _, err := log.Append(store.Entry[message.Message]{
			FormChannelVersion: board, Payload: msg,
		}); err != nil {
			t.Fatal(err)
		}
	}
	prose := func(text string) message.Message {
		return message.Message{Role: message.RoleInput,
			Content: []message.Content{message.TextContent(text)}}
	}

	rendered := map[uint64][]string{}
	run := func() {
		if _, err := CatchUp(CatchUpConfig{
			Log: log, Translator: rows, Form: &oneKeyBoard{key: "mantra", at: 1},
			Encode: func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
				for _, p := range msg.Patches {
					for _, e := range p.Entries() {
						rendered[msg.LogicalTime] = append(rendered[msg.LogicalTime], e.Key)
					}
				}
				return []json.RawMessage{json.RawMessage(`{}`)}, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The first pass ends on a tool result, which defers the patch.
	add(prose("first question"), 0)
	add(message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ToolResultContent("c1", "bash", `set mantra = "named myself"`, false),
	}}, 1)
	run()
	if len(rendered) != 0 {
		t.Fatalf("the patch was rendered before anyone spoke: %v", rendered)
	}

	// THE COLD START: a new deriver, seeded only from what is on disk. This is
	// a fresh CatchUp over the same rows, which is exactly what a restarted
	// daemon does.
	add(prose("second question"), 1)
	run()

	if got := rendered[3]; len(got) != 1 || got[0] != "mantra" {
		t.Fatalf("after a cold start the second question was given %v; want the deferred mantra patch", got)
	}
}
