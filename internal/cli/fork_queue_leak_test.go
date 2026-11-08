package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/sdk/aria"
)

// Gluck, 0.57.4: forked a figaro while a prompt waited in its queue, attended
// the child, and the child's queue pit listed the parent's prompt. The child
// never held it: the pager kept the parent's rows across the switch, because
// the child's empty queue compared equal to a list the switch had already
// emptied and so was never drawn.

const parentsQueuedPrompt = "a prompt the parent holds in its queue"

func queueSnapshot(rows map[uint64]string) form.Snapshot {
	set := map[string]json.RawMessage{}
	var order []uint64
	for id, text := range rows {
		order = append(order, id)
		key := "items." + strings.TrimSpace(jsonText(id)) + "."
		set[key+"state"], _ = json.Marshal("queued")
		set[key+"text"], _ = json.Marshal(text)
	}
	set["order"], _ = json.Marshal(order)
	set["epoch"], _ = json.Marshal("e1")
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func busyRuntime() form.Snapshot {
	state, _ := json.Marshal("thinking")
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"turn.state": state}, nil))
}

func onScreen(frames [][]string, text string) bool {
	if len(frames) == 0 {
		return false
	}
	for _, row := range frames[len(frames)-1] {
		if strings.Contains(row, text) {
			return true
		}
	}
	return false
}

func TestAForkDoesNotShowItsParentsQueue(t *testing.T) {
	tape := newFrameTape(100, 30)
	in := &interactiveInput{mu: new(renderLock), intrinsics: newIntrinsicMirrors()}
	in.lt = newLivelogTurn(tape, 100, 30, &renderSettings{}, "ariaAAAA", time.Now(),
		newSessionStatus("ariaAAAA", time.Now()), nil, dimRule)
	in.lt.enterTranscript()
	in.lt.apply(aria.Page{Parts: partsFor(1, 3, "PARENT")})
	in.lt.setMoreBefore(false)

	in.intrinsics.runtime.reset(busyRuntime(), 1, 1)
	in.intrinsics.queue.reset(queueSnapshot(map[uint64]string{1: parentsQueuedPrompt}), 1, 1)
	in.projectQueue()
	in.lt.tr.render()
	if !onScreen(tape.frames, parentsQueuedPrompt) {
		t.Fatal("fixture: the parent's queued prompt is not in its queue pit")
	}

	in.parkSubject("ariaAAAA")
	in.lt.retarget("ariaBBBB", newSessionStatus("ariaBBBB", time.Now()), 4)
	in.intrinsics.reset(1)
	in.forgetQueueDebuts()
	in.intrinsics.runtime.reset(busyRuntime(), 2, 1)
	in.intrinsics.queue.reset(queueSnapshot(nil), 2, 1)
	in.projectQueue()
	in.lt.tr.render()

	if onScreen(tape.frames, parentsQueuedPrompt) {
		t.Fatalf("the child's pager shows its parent's queued prompt:\n%s", strings.Join(tape.frames[len(tape.frames)-1], "\n"))
	}
	if n := len(in.lt.tr.queued); n != 0 {
		t.Fatalf("the child's pager holds %d queued row(s) from its parent", n)
	}

	pressKeys(in.lt.tr, "Q")
	in.lt.tr.render()
	if onScreen(tape.frames, parentsQueuedPrompt) {
		t.Fatalf("Q on the child opens a queue pit listing its parent's prompt:\n%s", strings.Join(tape.frames[len(tape.frames)-1], "\n"))
	}
}
