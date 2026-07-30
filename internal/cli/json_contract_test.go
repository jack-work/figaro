package cli

import (
	"strings"
	"testing"
)

// One contract for -j: submit, print one object, exit. It used to mean four

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

// Every combination that cannot honour -j must be REJECTED, not dropped.
func TestJSONArgvIsRejectedNotIgnored(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts sendOpts
		want string
	}{
		{"json with raw", sendOpts{json: true, raw: true}, "--raw"},
		{"json with verbatim", sendOpts{json: true, verbatim: true}, "--verbatim"},
		{"json with exec", sendOpts{json: true, exec: true}, "--exec"},
		{"json with listen", sendOpts{json: true, listen: true}, "--listen"},
		{"json with verbose", sendOpts{json: true, verbose: true}, "--verbose"},
		{"json with ephemeral", sendOpts{json: true, ephemeral: true}, "--ephemeral"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSendOpts(tc.opts, false)
			if err == nil {
				t.Fatal("accepted silently — the flag would be dropped")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message must name %s: %s", tc.want, err)
			}
		})
	}
}

// TestValidateSendOptsAcceptsTheHonourableForms is the other direction: the
// combinations that DO work must not start erroring.
func TestValidateSendOptsAcceptsTheHonourableForms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    sendOpts
		hasTurn bool
	}{
		{"bare json", sendOpts{json: true}, false},
		{"json plus forget is the same gesture", sendOpts{json: true, forget: true}, false},
		{"json on a fork-send", sendOpts{json: true}, true},
		{"raw alone", sendOpts{raw: true}, false},
		{"ephemeral raw", sendOpts{ephemeral: true, raw: true}, false},
		{"exec with -n", sendOpts{exec: true, dryRun: true}, false},
		{"exec with -y", sendOpts{exec: true, skipYes: true}, false},
		{"plain forget", sendOpts{forget: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSendOpts(tc.opts, tc.hasTurn); err != nil {
				t.Errorf("rejected a valid form: %s", err)
			}
		})
	}
}

// TestValidateSendOptsRejectsTheRest covers the contradictions that predate
// --json, so the extraction into a pure function did not lose any of them.
func TestValidateSendOptsRejectsTheRest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    sendOpts
		hasTurn bool
		want    string
	}{
		{"-n without exec", sendOpts{dryRun: true}, false, "only meaningful with --exec"},
		{"-y without exec", sendOpts{skipYes: true}, false, "only meaningful with --exec"},
		{"forget with exec", sendOpts{forget: true, exec: true}, false, "--forget contradicts"},
		{"forget with verbatim", sendOpts{forget: true, verbatim: true}, false, "--forget contradicts"},
		{"forget with ephemeral", sendOpts{forget: true, ephemeral: true}, false, "killed before the turn ran"},
		{"turn with ephemeral", sendOpts{ephemeral: true}, true, "not compatible"},
		{"turn with exec", sendOpts{exec: true}, true, "not compatible"},
		{"turn with verbatim", sendOpts{verbatim: true}, true, "not compatible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSendOpts(tc.opts, tc.hasTurn)
			if err == nil {
				t.Fatal("accepted silently")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got %s", tc.want, err)
			}
		})
	}
}
