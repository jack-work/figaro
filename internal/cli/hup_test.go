package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestHangupVerbs_HelpNamesTheDispositionAndTheOtherVerb(t *testing.T) {
	r := buildRouter("figaro", nil)

	for _, tc := range []struct {
		verb      string
		says      []string
		namesPeer string
	}{
		{verb: "hup", says: []string{"KEEP"}, namesPeer: "-d"},
		{verb: "cut", says: []string{"DISCARD"}, namesPeer: "hup"},
	} {
		cmd, ok := r.Command(tc.verb)
		if !ok {
			t.Fatalf("%s is not registered", tc.verb)
		}
		for _, want := range tc.says {
			if !strings.Contains(cmd.Short, want) {
				t.Errorf("%s: short help %q does not say %q: the queue disposition must be unmissable",
					tc.verb, cmd.Short, want)
			}
		}
		if !strings.Contains(cmd.Short, tc.namesPeer) {
			t.Errorf("%s: short help %q never mentions %q; a hangup that does not name the other disposition is half-documented",
				tc.verb, cmd.Short, tc.namesPeer)
		}
		if !strings.Contains(cmd.Long, tc.namesPeer) {
			t.Errorf("%s: long help never mentions %q", tc.verb, tc.namesPeer)
		}
		if !strings.Contains(cmd.Long, "-j") {
			t.Errorf("%s: long help must say how to get the messages back as JSON", tc.verb)
		}
	}
}

func TestHup_TakesTheDropFlag(t *testing.T) {
	r := buildRouter("figaro", nil)
	cmd, ok := r.Command("hup")
	if !ok {
		t.Fatal("hup is not registered")
	}
	found := false
	for _, f := range cmd.Flags {
		if f.Long == "drop-queued-messages" && f.Short == "d" && f.IsBool {
			found = true
		}
		if strings.HasPrefix(f.Long, "no-") {
			t.Errorf("hup grew a negated flag %q; the disposition is named, not un-named", f.Long)
		}
	}
	if !found {
		t.Error("hup has no -d/--drop-queued-messages flag")
	}
}

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

func TestQueueRowText_FlattensACombinedMessage(t *testing.T) {
	if got := queueRowText("one\ntwo\nthree"); got != "one …" {
		t.Errorf("queueRowText = %q, want %q", got, "one …")
	}
	if got := queueRowText("single"); got != "single" {
		t.Errorf("queueRowText = %q, want %q", got, "single")
	}
}

func TestQueueCount_NamesTheFold(t *testing.T) {
	one := []rpc.QueuedPrompt{{ID: 2, Text: "a\n\nb\n\nc", Merged: []uint64{3, 4}}}
	if got := queueCount(one); got != "1 message (folded from 3)" {
		t.Errorf("queueCount = %q, want %q", got, "1 message (folded from 3)")
	}
	plainTwo := []rpc.QueuedPrompt{{ID: 2, Text: "a"}, {ID: 3, Text: "b"}}
	if got := queueCount(plainTwo); got != "2 messages" {
		t.Errorf("queueCount = %q, want %q", got, "2 messages")
	}
	if got := queueCount(nil); got != "0 messages" {
		t.Errorf("queueCount = %q, want %q", got, "0 messages")
	}
}
