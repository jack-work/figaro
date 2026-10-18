package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
)

func TestCompleteAriaIDsAfterFlag_FiresOnlyAfterIDFlag(t *testing.T) {
	called := 0
	inner := func(*cmdkit.CompleteContext) []string {
		called++
		return []string{"INNER"}
	}
	fn := completeAriaIDsAfterFlag(inner)

	// With the daemon isolated, idCandidates returns nil: what is asserted
	// is that inner was not reached.
	isolateDaemonEnv(t)
	_ = fn(&cmdkit.CompleteContext{Args: []string{"--id"}})
	if called != 0 {
		t.Errorf("inner invoked when cursor after --id (calls=%d)", called)
	}

	got := fn(&cmdkit.CompleteContext{Args: []string{}})
	if called != 1 || len(got) != 1 || got[0] != "INNER" {
		t.Errorf("inner not invoked at first positional; calls=%d got=%v", called, got)
	}

	got = fn(&cmdkit.CompleteContext{Args: []string{"--id", "myid"}})
	if called != 2 || len(got) != 1 || got[0] != "INNER" {
		t.Errorf("inner not invoked after --id <value>; calls=%d got=%v", called, got)
	}
}

func TestCompleteAriaIDsAfterFlag_NilInnerSafe(t *testing.T) {
	fn := completeAriaIDsAfterFlag(nil)
	if got := fn(&cmdkit.CompleteContext{Args: []string{"foo"}}); got != nil {
		t.Errorf("expected nil with nil inner, got %v", got)
	}
}

func TestCompleteAriaIDsPositionalOrFlag_HandlesNil(t *testing.T) {
	isolateDaemonEnv(t)
	if got := completeAriaIDsPositionalOrFlag(nil); got != nil {
		t.Errorf("expected nil for nil ctx, got %v", got)
	}
	if got := completeAriaIDsPositionalOrFlag(&cmdkit.CompleteContext{Args: []string{"already"}}); got != nil {
		t.Errorf("expected nil mid-position, got %v", got)
	}
}

func TestIDCandidates_NeverPanics(t *testing.T) {
	// With or without a daemon, the call must return well under a second; the
	// value is not asserted.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = idCandidates(nil)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("idCandidates exceeded soft-deadline budget")
	}
}

// Completion offers what people type: the word handle, newest first, with the
// mantra beside it. Typed hex still completes to ids, the list is capped, and
// the ceremonial rows never appear.
func TestNameCandidatesOfferHandles(t *testing.T) {
	figs := []rpc.FigaroInfoResponse{
		{ID: "a5647bfbc216b8a09ffea9586393c289", Handle: "nursery-lioness", Short: "a5647", Mantra: "ids work", State: "active", LastActive: 30, Kind: "conversation"},
		{ID: "d9a9cebf", Handle: "suds-obtain", Short: "d9a9c", Mantra: "the impresario", LastActive: 20, Kind: "conversation"},
		{ID: "@f062f8d7", Handle: "@sycamore-tamper", Name: "impresario", LastActive: 10, Kind: "form"},
		{ID: "null", Kind: "null"},
		{ID: "opus5-ant", Kind: "outfit"},
	}
	desc := &cmdkit.CompleteContext{Describe: true}
	got := nameCandidates(desc, figs, "", "conversation", "form")
	want := []string{
		cmdkit.Candidate("nursery-lioness", "active · ids work"),
		cmdkit.Candidate("suds-obtain", "the impresario"),
		cmdkit.Candidate("@sycamore-tamper", "form · impresario"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bare completion:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := nameCandidates(nil, figs, "sud", "conversation", "form"); len(got) != 1 || got[0] != "suds-obtain" {
		t.Fatalf("typed words complete to the handle: %v", got)
	}
	if got := nameCandidates(nil, figs, "a56", "conversation", "form"); len(got) != 1 || got[0] != figs[0].ID {
		t.Fatalf("typed hex completes to the id: %v", got)
	}
	if got := nameCandidates(nil, figs, "@syc", "conversation", "form"); len(got) != 1 || got[0] != "@sycamore-tamper" {
		t.Fatalf("a form completes by its handle: %v", got)
	}
	if got := nameCandidates(nil, figs, "", "conversation"); len(got) != 2 {
		t.Fatalf("aria-only completion still offers forms: %v", got)
	}

	many := make([]rpc.FigaroInfoResponse, 3*idCandidatesBare)
	for i := range many {
		many[i] = rpc.FigaroInfoResponse{ID: fmt.Sprintf("%08x", i), Handle: fmt.Sprintf("word-%d", i), LastActive: int64(i)}
	}
	if got := nameCandidates(nil, many, "", "conversation"); len(got) != idCandidatesBare || got[0] != many[len(many)-1].Handle {
		t.Fatalf("bare completion offers the %d newest, got %d starting %q", idCandidatesBare, len(got), got[0])
	}
}
