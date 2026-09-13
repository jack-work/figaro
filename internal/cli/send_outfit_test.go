package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

// Three flags, one parser, three verbs. -O takes outfit NAMES, -S takes keys,
// -D takes removals; each composes on repeat, survives bundling, and is legal
// against an existing target (dressing is a form fold now, not a birth-only
// modifier). The axes are checked apart: a `k=v` under -O is a grammar error
// that names the flag which takes it.
func TestSendOutfitParses(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    string // the outfit names, as typed
		wantSet string // the -S text, as typed
		err     string
	}{
		{name: "long form", in: []string{"--outfit", "sonn5", "-r", "--", "p"}, want: "sonn5"},
		{name: "short form", in: []string{"-O", "sonn5", "-r", "--", "p"}, want: "sonn5"},
		{name: "inline form", in: []string{"--outfit=sonn5", "-r", "--", "p"}, want: "sonn5"},
		{name: "absent", in: []string{"-r", "--", "p"}, want: ""},
		{name: "comma folds", in: []string{"-O", "a,b", "--", "p"}, want: "a,b"},
		{name: "repeats fold", in: []string{"-O", "a", "-O", "b", "--", "p"}, want: "a,b"},
		{name: "sugar goes to -S", in: []string{"-S", "ttl=1h", "--", "p"}, wantSet: "ttl=1h"},
		{name: "literal goes to -S", in: []string{"-S", `{"ttl":"1h"}`, "--", "p"}, wantSet: `{"ttl":"1h"}`},
		{name: "both axes", in: []string{"-O", "a", "-S", "ttl=1h", "--", "p"}, want: "a", wantSet: "ttl=1h"},
		{name: "set repeats fold", in: []string{"-S", "a=1", "-S", "b=2", "--", "p"}, wantSet: "a=1,b=2"},
		{name: "sugar under -O is refused", in: []string{"-O", "ttl=1h", "--", "p"}, err: "goes in --set"},
		{name: "literal under -O is refused", in: []string{"-O", `{"ttl":"1h"}`, "--", "p"}, err: "goes in --set"},
		{name: "a name under -S is refused", in: []string{"-S", "sonn5", "--", "p"}, err: "-O sonn5"},
		{name: "against a target", in: []string{"--id", "abc12345", "-O", "a", "--", "p"}, want: "a"},
		{name: "bundled with value", in: []string{"-rvOsonn5", "--", "p"}, want: "sonn5"},
		{name: "bundled, value next", in: []string{"-rvO", "sonn5", "--", "p"}, want: "sonn5"},
		{name: "no value", in: []string{"--outfit", "--", "p"}, err: "--outfit requires a value"},
		{name: "empty inline", in: []string{"--outfit=", "--", "p"}, err: "--outfit requires a value"},
		{name: "bad name", in: []string{"-O", "../etc", "--", "p"}, err: "cannot contain"},
		{name: "bad literal", in: []string{"-S", "{oops}", "--", "p"}, err: "not a JSON object"},
		{name: "not gangable", in: []string{"-rL", "--", "p"}, err: "unknown flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, rest, err := extractSendFlags(tc.in)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := opts.outfitText; got != tc.want {
				t.Errorf("outfit names: got %q, want %q", got, tc.want)
			}
			if got := opts.setText; got != tc.wantSet {
				t.Errorf("set text: got %q, want %q", got, tc.wantSet)
			}
			if got := extractPrompt(rest); got != "p" {
				t.Errorf("prompt: got %q", got)
			}
		})
	}
}

// The wiring that matters: whatever the parser read must reach the prompt, in
// the SAME call as the text, and it must travel ON THE PLAN. Parsing used to
// write a package global, so a second command parsed while the first was in
// flight sent the first one's question wearing the second one's dressing.
func TestParsedOutfitRidesThePrompt(t *testing.T) {
	opts, _, err := extractSendFlags([]string{"-O", "a", "-S", "ttl=1h", "--", "p"})
	if err != nil {
		t.Fatal(err)
	}
	// A SECOND PARSE IS NOT THE FIRST ONE'S BUSINESS.
	other, _, err := extractSendFlags([]string{"-S", "ttl=9h", "--", "q"})
	if err != nil {
		t.Fatal(err)
	}
	in := buildPromptForm(opts.outfit)
	if in == nil || in.Patch == nil {
		t.Fatal("no form input")
	}
	// The names travel AS NAMES, in their own field: no directive is smuggled
	// through the patch, and the daemon resolves them at its API boundary.
	if len(in.Outfits) != 1 || in.Outfits[0] != "a" {
		t.Errorf("prompt outfits: %v", in.Outfits)
	}
	if _, leaked := in.Patch.Entry("layers"); leaked {
		t.Errorf("a layers directive reached the wire: %v", in.Patch.Entries())
	}
	if got := string(mustEntry(*in.Patch, "ttl")); got != `"1h"` {
		t.Errorf("prompt ttl: %q", got)
	}

	if got := string(mustEntry(*buildPromptForm(other.outfit).Patch, "ttl")); got != `"9h"` {
		t.Errorf("the second plan's ttl: %q", got)
	}
	// And the first plan still carries its own, parsed before the second.
	if got := string(mustEntry(*buildPromptForm(opts.outfit).Patch, "ttl")); got != `"1h"` {
		t.Errorf("the first plan wore the second's dressing: %q", got)
	}

	bare, _, err := extractSendFlags([]string{"--", "p"})
	if err != nil {
		t.Fatal(err)
	}
	if in := buildPromptForm(bare.outfit); in != nil && in.Patch != nil {
		t.Errorf("patch invented from nothing: %v", in.Patch)
	}
}

// `new` shares send's parser, so it must reject what it cannot honour rather
// than ignore it.
func TestNewRejectsSendOnlyFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"--id", "abc12345", "--", "p"}, "always creates"},
		{[]string{"-x", "--", "p"}, "--exec"},
		{[]string{"-O", "a", "--", "p"}, ""},
		{[]string{"-j", "--", "p"}, ""},
	} {
		opts, _, err := extractSendFlags(tc.args)
		if err != nil {
			t.Fatalf("parse %v: %v", tc.args, err)
		}
		err = validateNewOpts(opts)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("new %v: rejected a valid form: %s", tc.args, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("new %v: want %q, got %v", tc.args, tc.err, err)
		}
	}
}

// mustEntry is the value a patch sets at key, for tests that assert on one.
func mustEntry(p form.Patch, key string) json.RawMessage {
	e, ok := p.Entry(key)
	if !ok {
		return nil
	}
	return e.New
}

// THE REQUEST IS WHERE IT COUNTS. The reviewer's probe: parse one send, parse
// another, submit the FIRST, and read what the aria received. With the dressing
// in a package global the first question arrived wearing the second's -S.
func TestSubmittedQuestionWearsItsOwnPlansDressing(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "aria.sock")
	got := make(chan rpc.QuaRequest, 4)
	fakeRPCServer(t, sock, map[string]jkrpc.HandlerFunc{
		rpc.MethodQua: func(_ context.Context, params json.RawMessage) (interface{}, error) {
			var req rpc.QuaRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			got <- req
			return rpc.QuaResponse{OK: true}, nil
		},
	})

	first, err := planSend([]string{"-S", "mantra=first", "--", "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planSend([]string{"-S", "mantra=second", "--", "two"}); err != nil {
		t.Fatal(err)
	}

	fcli, err := sdk.DialAria(transport.UnixEndpoint(sock), nil)
	if err != nil {
		t.Fatalf("dial the fake aria: %v", err)
	}
	defer fcli.Close()
	if _, _, err := sendVerb(context.Background(), verbEnv{}, first, fcli, "aria1234"); err != nil {
		t.Fatalf("sendVerb: %v", err)
	}

	select {
	case req := <-got:
		if req.Text != "one" {
			t.Fatalf("the aria received %q", req.Text)
		}
		if req.Form == nil || req.Form.Patch == nil {
			t.Fatal("the question carried no dressing at all")
		}
		if v := string(mustEntry(*req.Form.Patch, "mantra")); v != `"first"` {
			t.Fatalf("the first plan's question wore %s", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fake aria was never asked")
	}
}
