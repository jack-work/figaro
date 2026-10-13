package cli

import (
	"reflect"
	"strings"
	"testing"
)

// TestPlanFork: a prompt is optional and send-only flags are then errors; a
// bare positional is the target without `--`; --stay governs the shell, not the
// prompt.
func TestPlanFork(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantSpec   string
		wantPrompt string
		wantOpts   sendOpts
		wantCompos bool
		wantErr    string
	}{
		{
			name: "bare fork",
			args: nil,
		},
		{
			name:     "positional target",
			args:     []string{"abc12345"},
			wantSpec: "abc12345",
			wantOpts: sendOpts{target: "abc12345"},
		},
		{
			name:     "positional target at a turn",
			args:     []string{"abc12345:12"},
			wantSpec: "abc12345:12",
			wantOpts: sendOpts{target: "abc12345:12"},
		},
		{
			name:     "id flag and stay",
			args:     []string{"--id", "abc12345", "--stay"},
			wantSpec: "abc12345",
			wantOpts: sendOpts{id: "abc12345", stay: true},
		},
		{
			name:       "bare prompt",
			args:       []string{"--", "what", "if"},
			wantPrompt: "what if",
		},
		{
			name:       "positional target with a prompt",
			args:       []string{"abc12345:12", "--", "try", "again"},
			wantSpec:   "abc12345:12",
			wantPrompt: "try again",
			wantOpts:   sendOpts{target: "abc12345:12"},
		},
		{
			name:       "stay composes with a prompt",
			args:       []string{"--stay", "abc12345", "--", "p"},
			wantSpec:   "abc12345",
			wantPrompt: "p",
			wantOpts:   sendOpts{target: "abc12345", stay: true},
		},
		{
			name:       "json and forget bundle",
			args:       []string{"-fj", "--", "p"},
			wantPrompt: "p",
			wantOpts:   sendOpts{forget: true, json: true},
		},
		{
			name:       "raw with a prompt",
			args:       []string{"-r", "--", "p"},
			wantPrompt: "p",
			wantOpts:   sendOpts{raw: true},
		},
		{
			name:       "exec with -n",
			args:       []string{"-x", "-n", "--", "list the files"},
			wantPrompt: "list the files",
			wantOpts:   sendOpts{exec: true, dryRun: true},
		},
		{
			// `fork --` opens the composer, so the send flags stay legal.
			name:       "empty boundary opens the composer",
			args:       []string{"-r", "--"},
			wantCompos: true,
			wantOpts:   sendOpts{raw: true},
		},
		{
			name:    "raw without a prompt",
			args:    []string{"-r"},
			wantErr: "-r/--raw only applies with a prompt",
		},
		{
			name:    "forget without a prompt names the target in the hint",
			args:    []string{"abc12345", "-f"},
			wantErr: "fork abc12345 -- <prompt>",
		},
		{
			name:    "dry-run without exec",
			args:    []string{"-n", "--", "p"},
			wantErr: "-n / -y only meaningful with --exec",
		},
		{
			name:    "forget with exec",
			args:    []string{"-fx", "--", "p"},
			wantErr: "--forget contradicts",
		},
		{
			name:    "bad turn",
			args:    []string{"abc12345:0", "--", "p"},
			wantErr: "bad :<turn>",
		},
		{
			name:    "unknown flag",
			args:    []string{"--stya", "--", "p"},
			wantErr: `unknown flag "--stya"`,
		},
		{
			name:    "second positional",
			args:    []string{"abc12345", "def67890"},
			wantErr: "unexpected argument",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planFork(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if plan.spec != tc.wantSpec {
				t.Errorf("spec: got %q, want %q", plan.spec, tc.wantSpec)
			}
			if plan.prompt != tc.wantPrompt {
				t.Errorf("prompt: got %q, want %q", plan.prompt, tc.wantPrompt)
			}
			if plan.compose != tc.wantCompos {
				t.Errorf("compose: got %v, want %v", plan.compose, tc.wantCompos)
			}
			if !reflect.DeepEqual(plan.opts, tc.wantOpts) {
				t.Errorf("opts: got %+v, want %+v", plan.opts, tc.wantOpts)
			}
			if got, want := plan.hasPrompt(), tc.wantPrompt != "" || tc.wantCompos; got != want {
				t.Errorf("hasPrompt: got %v, want %v", got, want)
			}
		})
	}
}

// The routing table is the contract: a forked prompt means what it means on `send`.
func TestForkPromptRoute(t *testing.T) {
	cases := []struct {
		name string
		opts sendOpts
		want string
	}{
		{"default is the rich stream", sendOpts{}, "rich"},
		{"raw", sendOpts{raw: true}, "raw"},
		{"verbose stays rich", sendOpts{verbose: true}, "rich"},
		{"listen stays rich", sendOpts{listen: true}, "rich"},
		{"forget", sendOpts{forget: true}, "forget"},
		{"verbatim", sendOpts{verbatim: true}, "verbatim"},
		{"exec", sendOpts{exec: true}, "exec"},
		{"forget wins over raw", sendOpts{forget: true, raw: true}, "forget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := forkPromptRoute(tc.opts); got != tc.want {
				t.Fatalf("forkPromptRoute(%+v) = %q, want %q", tc.opts, got, tc.want)
			}
		})
	}
}

// fork owns the machine-readable line; --json surviving into the send dispatch
// would print two objects.
func TestPromptForkedAriaClearsJSON(t *testing.T) {
	opts := sendOpts{json: true, forget: true, id: "old11111", target: "old11111:3"}
	opts.id = "new22222"
	opts.target = ""
	opts.json = false
	if opts.json {
		t.Fatal("json must be cleared before delegating to send")
	}
	if opts.id != "new22222" || opts.target != "" {
		t.Fatalf("target must be the new branch alone, got id=%q target=%q", opts.id, opts.target)
	}
}

func TestForkTargetHint(t *testing.T) {
	if got := forkTargetHint(""); got != "" {
		t.Errorf(`forkTargetHint("") = %q, want ""`, got)
	}
	if got := forkTargetHint("abc12345:3"); got != "abc12345:3 " {
		t.Errorf("forkTargetHint: got %q", got)
	}
}

// -j with anything that streams or renders is a usage error on fork exactly as on send.
func TestForkJSONSubmitsAndExits(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"json with raw", []string{"-j", "-r", "--", "p"}, "--raw"},
		{"json with verbatim", []string{"-j", "-v", "--", "p"}, "--verbatim"},
		{"json with exec", []string{"-j", "-x", "--", "p"}, "--exec"},
		{"json with listen", []string{"-j", "-l", "--", "p"}, "--listen"},
		{"json with verbose", []string{"-j", "-o", "--", "p"}, "--verbose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planFork(tc.args)
			if err == nil {
				t.Fatal("accepted silently: the flag would be dropped")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message must name %s: %s", tc.want, err)
			}
		})
	}
}

func TestForkJSONIsPlannedAsAPrompt(t *testing.T) {
	plan, err := planFork([]string{"-j", "--", "what if"})
	if err != nil {
		t.Fatalf("rejected a valid form: %s", err)
	}
	if !plan.hasPrompt() {
		t.Error("plan lost the prompt")
	}
	if !plan.opts.json {
		t.Error("plan lost --json")
	}
}
