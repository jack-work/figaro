package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/rpc"
)

// "idle" is a statement about the inbox. It says nothing about whether the
// last thing you asked for worked, and reporting only the first is exactly
// how a failed turn gets reported as a hang: three prompts in the log,
// nothing produced, and every surface saying idle.
func TestStatusReportsAFailedLastTurn(t *testing.T) {
	f := &rpc.FigaroInfoResponse{
		ID:             "94f0752b",
		State:          "idle",
		LastTurnReason: "error: anthropic: rate_limit_error (429): the provider asked us to wait 1h0m0s",
		LastTurnAt:     time.Now().Add(-9 * time.Minute).UnixMilli(),
	}
	got := lastTurnRow(f)
	if !strings.Contains(got, "rate_limit_error") {
		t.Fatalf("the reason must survive to the panel: %q", got)
	}
	if !strings.Contains(got, "ago") {
		t.Errorf("when it failed matters as much as that it failed: %q", got)
	}
}

// A status panel that reports every success teaches people to skim past the
// one line that mattered.
func TestStatusSaysNothingAboutAHealthyTurn(t *testing.T) {
	for _, reason := range []string{"", "end_turn", "stop_sequence", "max_tokens"} {
		f := &rpc.FigaroInfoResponse{State: "idle", LastTurnReason: reason}
		if got := lastTurnRow(f); got != "" {
			t.Errorf("reason %q produced a row %q; only trouble earns one", reason, got)
		}
	}
}

// An interrupt is not a failure but it is not a completion either, and a user
// coming back to a conversation deserves to know which one happened.
func TestStatusReportsAnInterruptedTurn(t *testing.T) {
	f := &rpc.FigaroInfoResponse{State: "idle", LastTurnReason: "interrupted"}
	if got := lastTurnRow(f); !strings.Contains(got, "interrupted") {
		t.Errorf("interrupted turn not reported: %q", got)
	}
}

func TestStatusRowSurvivesAMissingTimestamp(t *testing.T) {
	f := &rpc.FigaroInfoResponse{State: "idle", LastTurnReason: "error: boom"}
	if got := lastTurnRow(f); got != "error: boom" {
		t.Errorf("got %q", got)
	}
}
