package figaro

// THE INTRINSIC FORMS, ASSERTED WHERE THEY CAN FAIL.
//
// The defect these exist to prevent is a HOLE: between the drain loop lifting
// a message and the turn frame carrying it, the message used to be in neither
// the queue nor the transcript. It had ceased to exist anywhere a client could
// see, and that interval is what a reader experiences as latency. A faster
// wire would not have closed it.
//
// So the tests below are mostly about CONTINUITY rather than about values: at
// no point may a message be nowhere.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func newTestInbox(t *testing.T) *Inbox {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := NewInbox(ctx)
	t.Cleanup(b.Close)
	return b
}

// newTestAgentForQueue is an agent with only the parts these tests touch: an
// inbox and its intrinsic forms. A full NewAgent would need a provider and a store,
// neither of which a projection has any opinion about.
func newTestAgentForQueue(t *testing.T) *Agent {
	a := &Agent{id: "test", inbox: newTestInbox(t)}
	a.runtime = newIntrinsic(IntrinsicRuntime)
	a.queue = newIntrinsic(IntrinsicQueue)
	t.Cleanup(func() { a.runtime.close(); a.queue.close() })
	return a
}

// A intrinsic republishing UNCHANGED state must emit nothing. Every publisher
// states the whole truth every time it runs, so without this the queue would
// emit a patch on every tick of everything.
func TestIntrinsicRepublishIsSilent(t *testing.T) {
	c := newIntrinsic("test")
	defer c.close()

	c.publish(map[string]any{"turn": "idle"}, nil)
	_, first := c.Snapshot()
	c.publish(map[string]any{"turn": "idle"}, nil)
	_, second := c.Snapshot()
	if first != second {
		t.Fatalf("republishing identical state moved the version %d -> %d. Every "+
			"publisher states the WHOLE truth every time it runs, so a intrinsic that "+
			"treats an unchanged republish as news emits a patch per tick forever",
			first, second)
	}
	c.publish(map[string]any{"turn": "thinking"}, nil)
	_, third := c.Snapshot()
	if third == second {
		t.Fatal("a real change did not move the version")
	}
}

// The projection is TOTAL: whatever it no longer names must leave the form. A
// partial projection would leave rows nothing ever clears.
func TestQueueIntrinsicDropsWhatTheProjectionNoLongerNames(t *testing.T) {
	a := newTestAgentForQueue(t)

	a.publishQueue(InboxSnapshot{
		Epoch: "e1",
		Order: []uint64{1, 2},
		Items: []QueueItem{
			{ID: 1, Text: "one", State: rpc.QueueStateQueued},
			{ID: 2, Text: "two", State: rpc.QueueStateQueued},
		},
	})
	snap, _ := a.queue.Snapshot()
	if _, ok := snap.Get("items.2.text"); !ok {
		t.Fatal("the projection did not reach the form")
	}

	a.publishQueue(InboxSnapshot{
		Epoch: "e1",
		Order: []uint64{1},
		Items: []QueueItem{{ID: 1, Text: "one", State: rpc.QueueStateQueued}},
	})
	snap, _ = a.queue.Snapshot()
	if _, ok := snap.Get("items.2.text"); ok {
		t.Fatal("a message the projection no longer names is still in the form. The " +
			"projection is the whole truth every time it runs, and the form must " +
			"follow it down as well as up")
	}
	if _, ok := snap.Get("items.1.text"); !ok {
		t.Fatal("the surviving message was dropped too")
	}
}

// THE CENTRAL PROPERTY. Walk a message from accepted through committed and
// assert it is SOMEWHERE at every step -- never absent, never unstated.
func TestAMessageIsNeverNowhere(t *testing.T) {
	inbox := newTestInbox(t)

	var states []string
	record := func(snap InboxSnapshot) {
		found := ""
		for _, it := range snap.Items {
			if it.ID == 1 {
				found = string(it.State)
			}
		}
		if found == "" {
			// Before the message is sent it legitimately does not exist. The
			// property is that once it appears it never disappears again --
			// THE HOLE IS IN THE MIDDLE, not at the start.
			if len(states) > 0 {
				t.Errorf("message 1 was in a state and is now in NONE: it exists "+
					"nowhere a client could see it. States so far: %v", states)
			}
			return
		}
		if len(states) == 0 || states[len(states)-1] != found {
			states = append(states, found)
		}
	}
	inbox.OnChange(record)

	inbox.Send(event{typ: eventUserPrompt, text: "hello"})
	taken := inbox.TakeReadyUserPrompts()
	if len(taken) != 1 {
		// Recv is the other lift path; either is fine, but one must happen.
		evt, ok := inbox.Recv()
		if !ok {
			t.Fatal("nothing came off the inbox")
		}
		taken = []event{evt}
	}
	inbox.MarkCommitted(taken)
	inbox.MarkTurn([]uint64{1}, 42)

	want := []string{
		string(rpc.QueueStateQueued),
		string(rpc.QueueStateCommitting),
		string(rpc.QueueStateCommitted),
	}
	if len(states) != len(want) {
		t.Fatalf("the journey was %v, wanted %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("the journey was %v, wanted %v", states, want)
		}
	}

	// And the turn is stamped, which is how a client knows when its own
	// transcript has adopted the message and the row may be dropped.
	final := inbox.Project()
	for _, it := range final.Items {
		if it.ID == 1 && it.Turn != 42 {
			t.Fatalf("a committed message does not name the turn it became (got %d). "+
				"Without it a client must drop the row on a TIMER instead of on the fact",
				it.Turn)
		}
	}
}

// A deleted message says it was DELETED, and a drained one says it was
// DRAINED. They look identical in a list and mean opposite things to a reader
// watching their own work.
func TestDroppedAndDrainedAreDifferentFacts(t *testing.T) {
	inbox := newTestInbox(t)
	inbox.Send(event{typ: eventUserPrompt, text: "a"})
	inbox.Send(event{typ: eventUserPrompt, text: "b"})

	epoch := inbox.Epoch()
	inbox.DeletePrompts(epoch, []uint64{1}, false)
	inbox.DrainUserPrompts()

	got := map[uint64]rpc.QueueState{}
	for _, it := range inbox.Project().Items {
		got[it.ID] = it.State
	}
	if got[1] != rpc.QueueStateDropped {
		t.Fatalf("a user-deleted message reports %q, wanted %q", got[1], rpc.QueueStateDropped)
	}
	if got[2] != rpc.QueueStateDrained {
		t.Fatalf("a hangup-cleared message reports %q, wanted %q", got[2], rpc.QueueStateDrained)
	}
}

// An interrupt that folds a run of prompts must leave every folded id
// RESOLVABLE, so a client holding one can follow it to the survivor instead of
// watching its row vanish.
func TestMergedIdsRemainResolvable(t *testing.T) {
	inbox := newTestInbox(t)
	inbox.Send(event{typ: eventUserPrompt, text: "a"})
	inbox.Send(event{typ: eventUserPrompt, text: "b"})
	inbox.Send(event{typ: eventUserPrompt, text: "c"})
	inbox.CoalesceUserPromptRuns()

	byID := map[uint64]QueueItem{}
	for _, it := range inbox.Project().Items {
		byID[it.ID] = it
	}
	survivor := byID[1]
	if survivor.State != rpc.QueueStateQueued {
		t.Fatalf("the survivor is %q, wanted queued", survivor.State)
	}
	for _, id := range []uint64{2, 3} {
		it, ok := byID[id]
		if !ok {
			t.Fatalf("folded id %d resolves to nothing: a client holding it watches "+
				"its row vanish with no way to find where the message went", id)
		}
		if it.State != rpc.QueueStateMerged || it.Into != 1 {
			t.Fatalf("folded id %d reports %q into %d, wanted merged into 1",
				id, it.State, it.Into)
		}
	}
}

// The read surface must agree with the CRUD surface. QueuedPrompts used to
// stamp "queued" on every row unconditionally while the refusal messages knew
// better -- one surface denying what the other asserted.
func TestQueuedPromptsReportsCommitting(t *testing.T) {
	a := newTestAgentForQueue(t)
	a.inbox.Send(event{typ: eventUserPrompt, text: "hello"})
	taken := a.inbox.TakeReadyUserPrompts()
	if len(taken) == 0 {
		t.Fatal("nothing was lifted")
	}
	_, prompts := a.QueuedPrompts(true)
	if len(prompts) != 1 {
		t.Fatalf("a lifted message vanished from the listing entirely (%d rows). It is "+
			"still un-deletable, so the listing owes the reader its existence", len(prompts))
	}
	if prompts[0].State != rpc.QueueStateCommitting {
		t.Fatalf("a lifted message reports %q, wanted %q",
			prompts[0].State, rpc.QueueStateCommitting)
	}
}

// A INTRINSIC FORM MUST BE ABLE TO WRITE EVERY KEY IT PUBLISHES.
//
// THIS TEST EXISTS BECAUSE ITS ABSENCE SHIPPED A DEAD FEATURE. `model` is in
// the system-managed catalog (api/form.CheckWritable), which refuses an
// UNPRIVILEGED write so a human cannot type `figaro set model=...` into a
// board the harness owns. A intrinsic has no user-writable path at all, so the
// check had nothing to protect and could only refuse -- and it refused the
// WHOLE patch, not just that key. Every publish failed, every failure was a
// Warn nobody reads, and `fig form show <id>/runtime` answered `{}`.
//
// It was invisible to the unit tests because the test agent has NO MODEL:
// currentModel() returned "", the key was skipped, and the one key that could
// fail was never written. A fixture that is tidier than production is a
// fixture that certifies production untested -- so this one names the model
// explicitly, which is the only reason it can fail.
func TestRuntimeIntrinsicPublishesEveryKeyIncludingProtectedOnes(t *testing.T) {
	a := newTestAgentForQueue(t)
	a.form = testFormWithModel(t, "claude-test-5")
	if a.currentModel() != "claude-test-5" {
		t.Fatalf("the fixture does not carry a model (%q), so the one key that can be "+
			"REFUSED is never written and this test cannot fail for its stated reason",
			a.currentModel())
	}

	a.publishRuntime(rpc.RuntimeThinking, "")

	snap, version := a.runtime.Snapshot()
	if version == 0 {
		t.Fatal("publishing the runtime state landed NOTHING. The intrinsic is empty, " +
			"which is indistinguishable from a intrinsic with nothing to say")
	}
	for _, key := range []string{"turn.state", "turn.since", "inflight", "epoch"} {
		if _, ok := snap.Get(key); !ok {
			t.Fatalf("key %q is missing: the patch was refused whole, so the keys that "+
				"WERE writable went down with the one that was not", key)
		}
	}
	if got, ok := stringOf(snap, "turn.state"); !ok || got != string(rpc.RuntimeThinking) {
		t.Fatalf("turn.state is %q, wanted %q", got, rpc.RuntimeThinking)
	}
	if got, ok := stringOf(snap, "model"); !ok || got != "claude-test-5" {
		t.Fatalf("model is %q: a system-managed key the intrinsic owns was not written. "+
			"The protection catalog guards BOARDS from hand-editing; a intrinsic has "+
			"no hand to guard against", got)
	}
}

// Idle carries the verdict; every other state must CLEAR it, or a reason
// attached to a running turn describes the previous one and reads as if it
// described this one.
func TestRuntimeReasonIsClearedWhenTheTurnMovesOn(t *testing.T) {
	a := newTestAgentForQueue(t)

	a.publishRuntime(rpc.RuntimeIdle, "error: provider failed")
	snap, _ := a.runtime.Snapshot()
	if got, _ := stringOf(snap, "turn.reason"); got != "error: provider failed" {
		t.Fatalf("idle did not carry its verdict: %q", got)
	}

	a.publishRuntime(rpc.RuntimeThinking, "")
	snap, _ = a.runtime.Snapshot()
	if _, ok := snap.Get("turn.reason"); ok {
		t.Fatal("a running turn is still wearing the PREVIOUS turn's verdict, which " +
			"reads as if it were its own")
	}
}

// testFormWithModel is a board carrying system.model. THE MODEL IS THE POINT:
// it is a system-managed key, so it is the one thing in the runtime patch that
// an unprivileged write is refused for.
func testFormWithModel(t *testing.T, model string) *form.State {
	t.Helper()
	st, err := form.Open(filepath.Join(t.TempDir(), "form.json"))
	if err != nil {
		t.Fatalf("open form: %v", err)
	}
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	st.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": raw}, nil))
	return st
}

func stringOf(snap form.Snapshot, key string) (string, bool) {
	raw, ok := snap.Get(key)
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// readSources concatenates package sources for the call-site assertions.
func readSources(t *testing.T, names ...string) string {
	t.Helper()
	var b strings.Builder
	for _, n := range names {
		data, err := os.ReadFile(n)
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		b.Write(data)
	}
	return b.String()
}

func containsOutsideOfDecl(src, needle string) bool { return strings.Contains(src, needle) }

// A COALESCED RUN LEAVES THE QUEUE WHOLE, or it leaves half of it behind.
//
// MEASURED: two messages queued, both acknowledged, ONE removed. A contiguous
// run is LIFTED as N events and COMMITTED as one, so dropping only the
// survivor's id from `lifted` stranded every folded id there forever -- still
// reported as "committing", still drawn as in-flight, while the SAME id was
// also reported as "merged" by the survivor. Two rows, two states, one
// message, and nothing able to clear either.
func TestACoalescedRunLeavesTheQueueEntirely(t *testing.T) {
	inbox := newTestInbox(t)
	inbox.Send(event{typ: eventUserPrompt, text: "a"})
	inbox.Send(event{typ: eventUserPrompt, text: "b"})
	inbox.Send(event{typ: eventUserPrompt, text: "c"})

	taken := inbox.TakeReadyUserPrompts()
	if len(taken) != 3 {
		t.Fatalf("lifted %d events, wanted 3", len(taken))
	}
	merged, ok := mergePromptEvents(taken)
	if !ok {
		t.Fatal("the run did not merge")
	}
	inbox.MarkCommitted([]event{merged})

	states := map[uint64][]rpc.QueueState{}
	for _, it := range inbox.Project().Items {
		states[it.ID] = append(states[it.ID], it.State)
	}
	for id := uint64(1); id <= 3; id++ {
		got := states[id]
		if len(got) != 1 {
			t.Fatalf("message %d is reported in %d states at once (%v). One message is "+
				"one row; a client cannot reconcile two", id, len(got), got)
		}
		if got[0] == rpc.QueueStateQueued || got[0] == rpc.QueueStateCommitting {
			t.Fatalf("message %d is still IN FLIGHT (%q) after its turn committed, so its "+
				"row never leaves the drawer", id, got[0])
		}
	}
}
