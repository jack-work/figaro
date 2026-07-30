package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/cmdkit"
)

// Exit-code contract: 1 = it ran and failed, 2 = argv was rejected. The

// captureExit swaps the process-exit hook and returns the code die/dieUsage
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

// Proves the WIRING (rejected argv -> exit 2) with shapes that die inside
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
