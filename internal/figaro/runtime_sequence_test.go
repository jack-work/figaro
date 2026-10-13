package figaro

// The runtime intrinsic's publish half, with no daemon, provider, or terminal.

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// recordRuntime returns the ordered, deduped sequence of turn states published.
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

	// The transitions the turn loop drives, in its order; the loop's call sites
	// are asserted separately below.
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

// Every state must have a call site in the turn loop; asserted against the
// source because a real tool round cannot run in CI.
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

// No key may be both a leaf and a branch prefix: form.Build yields an identity
// patch for the losing write and nothing raises. Asserted over every published key.
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
