package cli

import "testing"

// One contract for -j/--json: submit the prompt, print exactly one object
// on stdout, exit.
//
// WHAT WAS WRONG. The same word meant four things:
//
//	send -j -- hi        parsed, then SILENTLY IGNORED (opts.json was read
//	                     only in runSendForget and runSendForkAt)
//	new -j               fired a one-shot Qua and returned   (the right one)
//	send <id>:<turn> -j  printed the object AND THEN streamed the rendered
//	                     turn to the same stdout — unparseable for `| jq`
//	list --json          rejected every other flag
//
// The first is the one that cost most: -j on the hottest verb did nothing
// at all, and said nothing about it.

func TestJSONImpliesSubmitAndExit(t *testing.T) {
	// The rule, at the level it is decided: -j turns a send into a
	// submit-and-exit, which is exactly what --forget already does.
	opts := sendOpts{json: true}
	if bad := jsonIncompatible(opts); bad != "" {
		t.Fatalf("a bare -j must be honourable, got %q", bad)
	}
}

func TestJSONIncompatibleNamesTheOffender(t *testing.T) {
	cases := []struct {
		name string
		opts sendOpts
		want string
	}{
		{"raw streams", sendOpts{json: true, raw: true}, "--raw"},
		{"verbatim streams frames", sendOpts{json: true, verbatim: true}, "--verbatim"},
		{"exec owns stdout", sendOpts{json: true, exec: true}, "--exec"},
		{"listen takes the terminal", sendOpts{json: true, listen: true}, "--listen"},
		{"verbose shapes a render that will not happen", sendOpts{json: true, verbose: true}, "--verbose"},
		{"plain json is fine", sendOpts{json: true}, ""},
		{"forget plus json is the same gesture", sendOpts{json: true, forget: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonIncompatible(tc.opts); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJSONArgvIsRejectedNotIgnored walks the real dispatcher: every
// combination that cannot honour -j must EXIT 2, not quietly drop a flag.
func TestJSONArgvIsRejectedNotIgnored(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"json with raw", []string{"-j", "-r", "--", "hi"}},
		{"json with verbatim", []string{"-j", "-v", "--", "hi"}},
		{"json with exec", []string{"-j", "-x", "--", "ls"}},
		{"json with listen", []string{"-j", "-l", "--", "hi"}},
		{"json with verbose", []string{"-j", "-o", "--", "hi"}},
		{"json with ephemeral", []string{"-j", "-e", "--", "hi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, exited := captureExit(t, func() { runSendAs(nil, "send", tc.args) })
			if !exited {
				t.Fatal("expected rejection; the call returned (flag silently dropped?)")
			}
			if code != 2 {
				t.Errorf("exit %d, want 2 (misuse)", code)
			}
		})
	}
}
