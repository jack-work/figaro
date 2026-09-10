package figaro

// DOES THE RUNTIME INTRINSIC FORM ACTUALLY MOVE?
//
// Gluck reported `<id>/runtime` reading "idle" while the model was streaming
// and while a tool was running. There are two candidate causes and they need
// separating before anything is fixed: the states are not PUBLISHED, or they
// are published and not READ. This test owns the publish half, deterministically
// and for free -- no daemon, no provider, no terminal.

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// recordRuntime subscribes to the runtime intrinsic form and returns a
// function yielding the ORDERED, DEDUPED sequence of turn states it published.
func recordRuntime(t *testing.T, a *Agent) func() []string {
	t.Helper()
	var seq []string
	a.runtime.form.OnCommit(func(_ uint64, patch form.Patch) {
		ent, ok := patch.Entry("turn.state")
		if !ok {
			return
		}
		s := ent.NewString()
		if s == "" {
			return
		}
		if len(seq) == 0 || seq[len(seq)-1] != s {
			seq = append(seq, s)
		}
	})
	return func() []string { return seq }
}

func TestRuntimePublishesEveryTransitionOfATurn(t *testing.T) {
	a := newTestAgentForQueue(t)
	a.form = testFormWithModel(t, "mock-model-v1")
	seq := recordRuntime(t, a)

	// Drive the transitions the turn loop drives, in the order it drives them.
	// This is a unit of the PUBLISHER, not of the loop: the loop's call sites
	// are asserted separately by TestEveryRuntimeStateHasACallSite below.
	a.publishRuntime(rpc.RuntimeAccepted, "")
	a.publishRuntime(rpc.RuntimeCommitting, "")
	a.publishRuntime(rpc.RuntimeThinking, "")
	a.publishRuntime(rpc.RuntimeTooling, "")
	a.publishRuntime(rpc.RuntimeThinking, "")
	a.publishRuntime(rpc.RuntimeIdle, "stop")

	want := []string{"accepted", "committing", "thinking", "tooling", "thinking", "idle"}
	got := seq()
	if len(got) != len(want) {
		t.Fatalf("the runtime form published %v, wanted %v.\nA state that never reaches "+
			"the form is a state no client can ever see, and the bar falls back to guessing",
			got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the runtime form published %v, wanted %v", got, want)
		}
	}
}

// EVERY STATE MUST HAVE A CALL SITE. The publisher working is worth nothing if
// the turn loop never invokes it for a given state -- which is exactly the
// shape of "idle while a tool runs": tooling is publishable and unpublished.
//
// Asserted against the SOURCE, because the alternative is driving a real
// provider through a real tool round, and a test that cannot run in CI is a
// test that does not run.
func TestEveryRuntimeStateHasACallSite(t *testing.T) {
	src := readSources(t, "turn.go", "agent.go", "intrinsic.go")
	for _, state := range []string{
		"RuntimeAccepted", "RuntimeCommitting", "RuntimeThinking",
		"RuntimeTooling", "RuntimeIdle",
	} {
		needle := "publishRuntime(rpc." + state
		if !containsOutsideOfDecl(src, needle) {
			t.Errorf("no call site publishes %s. The state exists in the vocabulary and "+
				"nothing ever announces it, which is how QueueStateCommitting spent "+
				"months as a constant nobody wrote", state)
		}
	}
}

// NO KEY MAY BE BOTH A LEAF AND A BRANCH PREFIX.
//
// This is the general form of the bug above, and it is invisible at the call
// site: form.Build produces an IDENTITY patch for the losing write, an identity
// patch is legitimately not news, and nothing anywhere raises an error. The
// symptom is a field frozen at its first value while its siblings update
// normally -- which reads as "the feature half works" rather than as a bug.
//
// Asserted over every key an intrinsic form publishes, so the next key added
// cannot reintroduce it.
func TestNoIntrinsicKeyShadowsAnother(t *testing.T) {
	a := newTestAgentForQueue(t)
	a.form = testFormWithModel(t, "mock-model-v1")

	a.publishRuntime(rpc.RuntimeThinking, "a reason")
	runtimeSnap, _ := a.runtime.Snapshot()

	a.publishQueue(InboxSnapshot{
		Epoch: "e", Order: []uint64{1},
		Items: []QueueItem{{ID: 1, Text: "x", Sender: "s", State: rpc.QueueStateQueued,
			At: 1, Merged: []uint64{2}, Into: 3, Turn: 4}},
	})
	queueSnap, _ := a.queue.Snapshot()

	for name, snap := range map[string]form.Snapshot{"runtime": runtimeSnap, "queue": queueSnap} {
		var keys []string
		for k := range snap.All() {
			keys = append(keys, k)
		}
		for _, a := range keys {
			for _, b := range keys {
				if a == b {
					continue
				}
				if strings.HasPrefix(b, a+".") {
					t.Errorf("%s: key %q is a LEAF and also the branch prefix of %q. "+
						"The scalar write to %q will be silently dropped once %q exists, "+
						"and the field will freeze at its first value while its siblings "+
						"keep updating. Rename it to %q.", name, a, b, a, b, a+".state")
				}
			}
		}
	}
}
