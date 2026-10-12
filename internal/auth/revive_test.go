package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	hush "github.com/jack-work/hush/client"
)

// goneErr is the error a real hush client returns when no agent serves its
// socket: the exact shape Gluck's turns failed with, produced by the real
// client rather than spelled by hand.
func goneErr(t *testing.T) error {
	t.Helper()
	c := hush.NewWithSocket(filepath.Join(t.TempDir(), "agent.sock"))
	_, _, err := c.OAuthGetFull("copilot")
	if err == nil {
		t.Fatal("a client with no agent behind it answered")
	}
	if !strings.Contains(err.Error(), "no such file or directory") {
		t.Fatalf("unexpected shape for a missing agent: %v", err)
	}
	return err
}

func TestAgentGoneRecognisesTheRealClientError(t *testing.T) {
	if !AgentGone(goneErr(t)) {
		t.Fatal("the error a missing agent produces is not recognised as a missing agent")
	}
	for _, other := range []error{
		hush.ErrOAuthNotFound,
		errors.New("oauth refresh rejected"),
		nil,
	} {
		if AgentGone(other) {
			t.Fatalf("%v was taken for a missing agent", other)
		}
	}
}

// A call that finds the agent gone revives it ONCE and asks again; the turn
// sees only the second answer.
func TestWithAgentRevivesAndRetriesOnce(t *testing.T) {
	gone := goneErr(t)
	calls, revives := 0, 0
	got, err := WithAgent(func() error { revives++; return nil }, func() (string, error) {
		calls++
		if calls == 1 {
			return "", gone
		}
		return "token", nil
	})
	if err != nil || got != "token" {
		t.Fatalf("got (%q, %v), want the token from the retry", got, err)
	}
	if calls != 2 || revives != 1 {
		t.Fatalf("%d calls and %d revives, want 2 and 1", calls, revives)
	}
}

// Anything that is not a missing agent is the agent's answer, and is not
// second-guessed; nor is a missing agent retried with nothing to revive it.
func TestWithAgentLeavesOtherFailuresAlone(t *testing.T) {
	revives := 0
	revive := func() error { revives++; return nil }
	_, err := WithAgent(revive, func() (string, error) { return "", hush.ErrOAuthNotFound })
	if !errors.Is(err, hush.ErrOAuthNotFound) || revives != 0 {
		t.Fatalf("a real answer was retried (revives=%d, err=%v)", revives, err)
	}
	gone := goneErr(t)
	calls := 0
	_, err = WithAgent[string](nil, func() (string, error) { calls++; return "", gone })
	if calls != 1 || !AgentGone(err) {
		t.Fatalf("with no reviver: %d calls, err %v; want one call and the original error", calls, err)
	}
}

// A revival that fails keeps the original error AND says the agent would not
// start, so the message for the user can name the vault, not the provider.
func TestWithAgentReportsAFailedRevival(t *testing.T) {
	gone := goneErr(t)
	_, err := WithAgent(func() error { return errors.New("unlock figaro: keyring locked") }, func() (string, error) {
		return "", gone
	})
	var rev *ReviveError
	if !errors.As(err, &rev) || !AgentGone(err) {
		t.Fatalf("want both the dial error and a ReviveError, got %v", err)
	}
	if !strings.Contains(err.Error(), "hush agent would not start: unlock figaro: keyring locked") {
		t.Fatalf("message does not name the revival: %v", err)
	}
}

// The strategy itself, end to end through the real client: the first ask
// finds no agent, Revive is called, and the error that reaches the caller
// after a failed revival is the one the hint recognises.
func TestOAuthRevivesThroughTheRealClient(t *testing.T) {
	revives := 0
	o := &OAuth{
		Hush:   hush.NewWithSocket(filepath.Join(t.TempDir(), "agent.sock")),
		Name:   "copilot",
		Revive: func() error { revives++; return errors.New("no identity") },
	}
	_, ok, err := o.TryResolve()
	if ok || err == nil {
		t.Fatalf("resolved with no agent: ok=%v err=%v", ok, err)
	}
	if revives != 1 {
		t.Fatalf("the strategy tried to revive %d times, want 1", revives)
	}
	if !strings.Contains(err.Error(), "hush agent would not start") {
		t.Fatalf("the failed revival is not in the error: %v", err)
	}
}
