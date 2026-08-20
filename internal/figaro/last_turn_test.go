package figaro

import (
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// The state figaro had no word for: prompts consumed off the queue (so
// `queue ls` is empty) and answered by nothing (so the log ends in user
// messages), while status reported idle. Every surface individually truthful,
// the aggregate a lie.
func TestUnansweredInputsCountsTheTail(t *testing.T) {
	a := &Agent{figLog: store.NewMemLog[message.Message]()}
	appendRole := func(role message.Role) {
		if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{Role: role}}); err != nil {
			t.Fatal(err)
		}
	}

	if got := a.unansweredInputs(); got != 0 {
		t.Errorf("empty log: got %d, want 0", got)
	}

	appendRole(message.RoleInput)
	appendRole(message.RoleOutput)
	if got := a.unansweredInputs(); got != 0 {
		t.Errorf("an answered prompt is not unanswered: got %d", got)
	}

	appendRole(message.RoleInput)
	appendRole(message.RoleInput)
	appendRole(message.RoleInput)
	if got := a.unansweredInputs(); got != 3 {
		t.Errorf("got %d, want 3 - this is the number that says work was taken and nothing produced", got)
	}

	// An assistant reply clears it.
	appendRole(message.RoleOutput)
	if got := a.unansweredInputs(); got != 0 {
		t.Errorf("got %d, want 0 after a reply", got)
	}
}

// The scan is bounded: only the tail can be unanswered, and an aria with more
// than the scan depth of unanswered prompts is already alarming enough to act
// on without an unbounded read.
func TestUnansweredInputsIsBounded(t *testing.T) {
	a := &Agent{figLog: store.NewMemLog[message.Message]()}
	for i := 0; i < unansweredScanDepth*2; i++ {
		if _, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.unansweredInputs(); got != unansweredScanDepth {
		t.Errorf("got %d, want the scan depth %d", got, unansweredScanDepth)
	}
}

func TestUnansweredInputsToleratesNoLog(t *testing.T) {
	a := &Agent{}
	if got := a.unansweredInputs(); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestLastTurnFailedReadsTheReason(t *testing.T) {
	for reason, want := range map[string]bool{
		"":                     false,
		"end_turn":             false,
		"interrupted":          false,
		"error: boom":          true,
		"error: anthropic 429": true,
	} {
		if got := (FigaroInfo{LastTurnReason: reason}).LastTurnFailed(); got != want {
			t.Errorf("LastTurnFailed(%q) = %v, want %v", reason, got, want)
		}
	}
}
