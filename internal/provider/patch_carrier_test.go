package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// A FORM PATCH IS NOT SOMEBODY SPEAKING.
//
// `figaro set` is documented as a silent write: "Patch a form key (no LLM
// round-trip)". Gluck's statement of the intent, 2026-09-28: "patches should
// not correspond to a response from figaro or wake it up or begin its loop,
// just instead patch silently and get picked up on the next message."
//
// A patch reaches the model as a <system-reminder>, and a reminder is rendered
// into whichever record the form cursor lands on. Three of those records
// cannot carry one honestly:
//
//   - an ASSISTANT record, where no encoder renders patches at all, so the
//     delta is folded into the board, the cursor advances, and the model is
//     never told. Found in the wild: aria 055879c4 LT 469.
//   - a record with NO CONTENT, which becomes a user message holding nothing
//     but the reminder. 835 such records exist in the author's store.
//   - a TOOL RESULT, where the reminder is the only text in a user-role
//     message. A tool_result block reads as tool output, so the only thing
//     the model can read as the user's own voice is the reminder, and it
//     concludes that the user spoke and said nothing. Found in the wild, in
//     the model's own words at 055879c4 LT 471: "This message is just an
//     empty system reminder with no actual user content to respond to."
//
// So a patch rides the next record the model reads as INPUT FROM A PERSON,
// and nothing else. That is the whole rule, and it is the sentence above.
func TestAPatchRidesTheNextRecordAPersonSpeaksIn(t *testing.T) {
	log := store.NewMemLog[message.Message]()
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

	// The turn the report describes: a question, an answer that calls a tool
	// to set the aria's own mantra, the tool's result, and then the next
	// question. The board advances to version 1 while the tool round is in
	// flight, so the cursor lands on the assistant record.
	add(prose("first question"), 0)
	add(message.Message{Role: message.RoleOutput, Content: []message.Content{
		message.TextContent("naming myself now"),
		{Type: message.ContentToolInvoke, ToolCallID: "c1", ToolName: "bash"},
	}}, 1)
	add(message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ToolResultContent("c1", "bash", `set mantra = "named myself"`, false),
	}}, 1)
	// A record with no content at all, which is what a patch-only submit
	// leaves behind.
	add(message.Message{Role: message.RoleInput}, 1)
	add(prose("second question"), 1)

	rendered := map[uint64][]string{}
	stats, err := CatchUp(CatchUpConfig{
		Log:        log,
		Translator: store.NewMemLog[[]json.RawMessage](),
		Form:       &oneKeyBoard{key: "mantra", at: 1},
		Encode: func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			for _, p := range msg.Patches {
				for _, e := range p.Entries() {
					rendered[msg.LogicalTime] = append(rendered[msg.LogicalTime], e.Key)
				}
			}
			// Every record here has something to encode except the empty one,
			// which is the case the encoders already drop.
			if len(msg.Content) == 0 && len(msg.Patches) == 0 {
				return nil, nil
			}
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = stats

	for lt, keys := range rendered {
		if lt != 5 {
			t.Errorf("LT %d was given the patch (%v); only LT 5 speaks for a person", lt, keys)
		}
	}
	if got := rendered[5]; len(got) != 1 || got[0] != "mantra" {
		t.Fatalf("LT 5, the next question, was given %v; want the pending mantra patch exactly once", got)
	}
}

// oneKeyBoard is a board holding ONE patch, landed at version `at`. It answers
// the (after, upTo] range the deriver asks for, so a test can see exactly
// which record the patch was offered to.
type oneKeyBoard struct {
	key string
	at  uint64
}

func (b *oneKeyBoard) PatchesBetween(after, upTo uint64) []message.Patch {
	if b.at <= after || b.at > upTo {
		return nil
	}
	return []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{
		b.key: json.RawMessage(`"named myself"`),
	}, nil)}
}
