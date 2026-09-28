package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// A SUBMISSION WITH NOTHING TO SAY IS NOT A TURN.
//
// `figaro set` is a silent write, and a submission carrying a form patch and
// no text is that same write wearing the prompt verb's clothes: `fork -S
// ttl=1h` is documented as "branch and patch it; say nothing yet", and an
// accidental Enter on an empty composer says nothing at all.
//
// Before this, SubmitPromptFrom queued the prompt event unconditionally. The
// drain loop lifted it, appendUserPrompt wrote a user record with NO CONTENT
// (and therefore no turn id, since the counter only moves for a record with
// something in it), and runTurn then drove a full provider round: the model
// handed a conversation ending in a user message that said nothing, and asked
// to answer it.
//
// The record is the visible half. 835 contentless input records exist across
// 544 arias in the author's store, each one a user turn the user never took.
// The provider round is the expensive half.
func TestAnEmptySubmissionQueuesNothing(t *testing.T) {
	cb, err := form.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cb.Close() })
	a := &Agent{id: "test", inbox: newTestInbox(t), form: cb}
	a.runtime = newIntrinsic(IntrinsicRuntime)
	a.queue = newIntrinsic(IntrinsicQueue)
	t.Cleanup(func() { a.runtime.close(); a.queue.close() })

	// No text. A form patch would ride along in production; what matters here
	// is that the turn loop is never handed anything.
	if err := a.SubmitPromptFrom(t.Context(), rpc.QuaRequest{Text: ""}, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, queued := a.QueuedPrompts(true); len(queued) != 0 {
		t.Fatalf("an empty submission queued %d message(s): %+v", len(queued), queued)
	}
	if snap := a.inbox.Project(); len(snap.Items) != 0 {
		t.Fatalf("an empty submission put %d item(s) in the inbox: %+v", len(snap.Items), snap.Items)
	}

	// The control, in the same shape: something to say IS a turn.
	if err := a.SubmitPromptFrom(t.Context(), rpc.QuaRequest{Text: "a real question"}, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, queued := a.QueuedPrompts(true); len(queued) != 1 {
		t.Fatalf("a real prompt queued %d message(s), want 1", len(queued))
	}
}

var _ = json.RawMessage(nil)
