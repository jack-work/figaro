package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/cmdkit"
)

// Exit-code contract: 1 = it ran and failed, 2 = argv was rejected.
//
// WHAT WAS WRONG. die() exited 1 and was what every hand-rolled PassRaw
// parser called; the router returned 2 for the identical class of mistake.
// Probed against the real binary before this split:
//
//	figaro ls --bogus           -> 2   "unknown flag: --bogus"
//	figaro send --bogus -- hi   -> 1   "unknown flag \"--bogus\""
//	figaro attend               -> 2   "requires at least 1 argument(s)"
//	figaro send hello           -> 1   "the prompt must follow `--`"
//
// Same question, two answers, split along a seam users cannot see. Any
// script distinguishing misuse (2) from runtime failure (1) — the getopt
// convention clig.dev documents — got a coin flip.

// captureExit swaps the process-exit hook and returns the code die/dieUsage
// asked for. The helpers do not return, so the recorder panics with a
// sentinel to unwind, exactly as os.Exit would have ended the call.
type exitPanic int

func captureExit(t *testing.T, fn func()) (code int, ok bool) {
	t.Helper()
	prev := exitProcess
	exitProcess = func(c int) { panic(exitPanic(c)) }
	defer func() {
		exitProcess = prev
		if r := recover(); r != nil {
			if c, is := r.(exitPanic); is {
				code, ok = int(c), true
				return
			}
			panic(r)
		}
	}()
	fn()
	return 0, false
}

func TestDieCodes(t *testing.T) {
	if code, ok := captureExit(t, func() { die("boom") }); !ok || code != 1 {
		t.Errorf("die: got code %d (exited=%v), want 1", code, ok)
	}
	if code, ok := captureExit(t, func() { dieUsage("bad argv") }); !ok || code != 2 {
		t.Errorf("dieUsage: got code %d (exited=%v), want 2", code, ok)
	}
}

// TestSendArgvRejectionExitsTwo proves the WIRING — a rejected argv becomes
// exit 2 — using shapes that die inside extractSendFlags, before dispatch
// exists as a possibility.
//
// It deliberately does NOT walk the flag-contradiction table through
// runSendAs. That is what lit the fork bomb on 2026-07-30: a dispatcher
// reached with a neutered guard falls through to mustConnectAngelus, which
// in a test binary re-execs the test binary as a daemon. The contradictions
// are asserted on the pure validator instead (json_contract_test.go).
func TestSendArgvRejectionExitsTwo(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"unknown long flag", []string{"--bogus", "--", "hi"}},
		{"unknown short flag", []string{"-Z", "--", "hi"}},
		{"prompt without the boundary", []string{"hello"}},
		{"bad turn coordinate", []string{"abc12345:0", "--", "hi"}},
		{"--id without a value", []string{"--id", "--", "hi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, exited := captureExit(t, func() { runSendAs(nil, "send", tc.args) })
			if !exited {
				t.Fatal("expected argv rejection, but the call returned")
			}
			if code != 2 {
				t.Errorf("exit code: got %d, want 2 (misuse)", code)
			}
		})
	}
}

// TestUnknownBareCommandExitsTwo covers the other door onto the same
// mistake: `figaro shwo -- x` is an unknown command, which the router
// answers with 2, so the bare-prompt form must not answer 1.
func TestUnknownBareCommandExitsTwo(t *testing.T) {
	router := buildRouter("figaro", nil)
	code, exited := captureExit(t, func() { unknownBareCommand("figaro", router, "shwo") })
	if !exited || code != 2 {
		t.Errorf("got code %d (exited=%v), want 2", code, exited)
	}
}

// TestRouterMisuseIsTwo is the other half of the contract, so the two
// halves cannot drift apart again: the router's own rejections stay at 2.
func TestRouterMisuseIsTwo(t *testing.T) {
	r := buildRouter("figaro", nil)
	r.Stdout, r.Stderr = &strings.Builder{}, &strings.Builder{}
	for _, args := range [][]string{
		{"ls", "--bogus"},
		{"attend"},
		{"snd"},
		nil,
	} {
		if code := r.Run(args); code != 2 {
			t.Errorf("router %v: got %d, want 2", args, code)
		}
	}
	var _ = cmdkit.FlagDef{}
}
