package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestQueueVerb_IsRegisteredWithItsFlags(t *testing.T) {
	r := buildRouter("figaro", nil)
	cmd, ok := r.Command("queue")
	if !ok {
		t.Fatal("queue is not registered")
	}
	want := map[string]bool{"id": false, "all": false, "json": false}
	for _, f := range cmd.Flags {
		if _, expected := want[f.Long]; expected {
			want[f.Long] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("queue has no --%s flag", name)
		}
	}
	// The sub-verb owns the positional slot, so the aria must be reachable by flag.
	if !strings.Contains(cmd.Long, "--id") {
		t.Error("queue's help must say how to address another aria")
	}
	if !strings.Contains(cmd.Long, "send") {
		t.Error("queue's help must point at `send` as the way to add to the queue")
	}
}

// A non-id is misuse (exit 2), not a skipped argument.
func TestParseQueueIDs_RejectsNonIDs(t *testing.T) {
	got := parseQueueIDs([]string{"3", "12"})
	if len(got) != 2 || got[0] != 3 || got[1] != 12 {
		t.Fatalf("parseQueueIDs = %v, want [3 12]", got)
	}

	for _, bad := range []string{"abc", "0", "-1", "3.5", ""} {
		code, exited := captureExit(t, func() { parseQueueIDs([]string{bad}) })
		if !exited || code != 2 {
			t.Errorf("parseQueueIDs(%q) exited %d (exited=%v), want 2 (misuse)", bad, code, exited)
		}
	}
}

// A refusal is a runtime outcome (1), never misuse (2).
func TestReportQueueResults_ExitCodes(t *testing.T) {
	applied := []rpc.QueueResult{
		{ID: 1, Outcome: rpc.QueueDeleted},
		{ID: 2, Outcome: rpc.QueueDeleted},
	}
	if code, exited := captureExit(t, func() { reportQueueResults("a", "e", applied, false) }); exited {
		t.Errorf("all applied exited %d; a fully applied request must not exit non-zero", code)
	}

	mixed := []rpc.QueueResult{
		{ID: 1, Outcome: rpc.QueueDeleted},
		{ID: 2, Outcome: rpc.QueueRejected, Reason: rpc.RejectCommitted, Detail: "already part of the conversation"},
	}
	if code, exited := captureExit(t, func() { reportQueueResults("a", "e", mixed, false) }); !exited || code != 1 {
		t.Errorf("a refusal exited %d (exited=%v), want 1 (runtime outcome, not misuse)", code, exited)
	}
}

// A refusal leaves through exitNow so the terminal-restore hooks run.
func TestReportQueueResults_RefusalRunsExitHooks(t *testing.T) {
	prevExit, prevHooks := exitProcess, exitHooks
	defer func() { exitProcess, exitHooks = prevExit, prevHooks }()
	restored, code := false, 0
	exitHooks = nil
	exitProcess = func(c int) { code = c }
	atExit(func() { restored = true })

	// The abort unwinds to a boundary, which is what runs the hooks.
	func() {
		defer RecoverExit()
		reportQueueResults("a", "e", []rpc.QueueResult{
			{ID: 1, Outcome: rpc.QueueRejected, Reason: rpc.RejectCommitted},
		}, false)
	}()

	if !restored {
		t.Error("the terminal-restore hook did not run on the refusal exit")
	}
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

// results is an array even when everything was refused: .results[] must not see null.
func TestQueueResultJSON_IsOneObject(t *testing.T) {
	b, err := json.Marshal(queueResultJSON{
		Aria:  "abc123",
		Epoch: "0123456789abcdef",
		Results: []rpc.QueueResult{
			{ID: 4, Outcome: rpc.QueueRejected, Reason: rpc.RejectMerged, Into: 3},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if strings.Count(got, "\n") != 0 {
		t.Errorf("one object, one line: %q", got)
	}
	if !strings.Contains(got, `"reason":"merged"`) || !strings.Contains(got, `"into":3`) {
		t.Errorf("a refusal must carry its reason and its redirect: %s", got)
	}
}

func TestQueueEditText_JoinsWithOrWithoutDashDash(t *testing.T) {
	if got := queueEditText([]string{"--", "hello", "world"}); got != "hello world" {
		t.Errorf("queueEditText = %q", got)
	}
	if got := queueEditText([]string{"hello", "world"}); got != "hello world" {
		t.Errorf("queueEditText = %q", got)
	}
}
