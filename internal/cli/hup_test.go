package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/rpc"
)

// hup and cut differ by ONE thing: what becomes of the queue. If a help line
// does not say so, the pair is two half-documented verbs and the user finds
// out which is which by losing something.

func TestHangupVerbs_HelpNamesTheDispositionAndTheOtherVerb(t *testing.T) {
	r := buildRouter("figaro", nil)

	for _, tc := range []struct {
		verb      string
		says      []string // the disposition, unmissably
		namesPeer string   // the other verb, so neither is discoverable alone
	}{
		{verb: "hup", says: []string{"KEEP"}, namesPeer: "cut"},
		{verb: "cut", says: []string{"DISCARD"}, namesPeer: "hup"},
	} {
		cmd, ok := r.Command(tc.verb)
		if !ok {
			t.Fatalf("%s is not registered", tc.verb)
		}
		for _, want := range tc.says {
			if !strings.Contains(cmd.Short, want) {
				t.Errorf("%s: short help %q does not say %q — the queue disposition must be unmissable",
					tc.verb, cmd.Short, want)
			}
		}
		if !strings.Contains(cmd.Short, tc.namesPeer) {
			t.Errorf("%s: short help %q never mentions %q; a pair of verbs that do not name each other is two half-documented verbs",
				tc.verb, cmd.Short, tc.namesPeer)
		}
		if !strings.Contains(cmd.Long, tc.namesPeer) {
			t.Errorf("%s: long help never mentions %q", tc.verb, tc.namesPeer)
		}
	}
}

// Both carry -j, and it is the same contract everywhere else in the CLI: one
// object on stdout, then exit.
func TestHangupVerbs_BothTakeJSON(t *testing.T) {
	r := buildRouter("figaro", nil)
	for _, verb := range []string{"hup", "cut"} {
		cmd, ok := r.Command(verb)
		if !ok {
			t.Fatalf("%s is not registered", verb)
		}
		found := false
		for _, f := range cmd.Flags {
			if f.Long == "json" && f.Short == "j" && f.IsBool {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no -j/--json flag", verb)
		}
	}
}

// `figaro cut -j > lost.json` has to be a SAVE. That means exactly one object,
// and a queue that is an array even when empty — a caller doing `.queue[]`
// must not have to special-case null.
func TestHangupJSON_IsOneObjectWithAnArrayQueue(t *testing.T) {
	b, err := json.Marshal(hangupJSON{
		Aria:    "abc123",
		Cleared: true,
		Epoch:   "0123456789abcdef",
		Queue:   []rpc.QueuedPrompt{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if strings.Contains(got, `"queue":null`) {
		t.Errorf("an empty queue must serialise as [], not null: %s", got)
	}
	if strings.Count(got, "\n") != 0 {
		t.Errorf("one object, one line: %q", got)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"aria", "cleared", "queue"} {
		if _, ok := back[key]; !ok {
			t.Errorf("the saved object must always carry %q: %s", key, got)
		}
	}
}

// A combined message is several lines; the manifest is one row per message.
func TestQueueRowText_FlattensACombinedMessage(t *testing.T) {
	if got := queueRowText("one\ntwo\nthree"); got != "one …" {
		t.Errorf("queueRowText = %q, want %q", got, "one …")
	}
	if got := queueRowText("single"); got != "single" {
		t.Errorf("queueRowText = %q, want %q", got, "single")
	}
}
