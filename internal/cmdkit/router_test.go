package cmdkit

import (
	"bytes"
	"testing"
)

func TestDispatchBasic(t *testing.T) {
	var called string
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	r.Register(&Command{
		Name:  "hello",
		Short: "say hello",
		Run: func(ctx *RunContext) error {
			called = "hello"
			return nil
		},
	})

	code := r.Run([]string{"hello"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if called != "hello" {
		t.Fatalf("expected hello called, got %q", called)
	}
}

func TestDispatchAlias(t *testing.T) {
	var called string
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	r.Register(&Command{
		Name:    "stop",
		Aliases: []string{"rest"},
		Short:   "stop the daemon",
		Run: func(ctx *RunContext) error {
			called = "stop"
			return nil
		},
	})

	code := r.Run([]string{"rest"})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if called != "stop" {
		t.Fatal("alias not dispatched")
	}
}

func TestFlagParsing(t *testing.T) {
	var ctx *RunContext
	buf := &bytes.Buffer{}
	r := NewRouter("test")
	r.Stderr = buf
	r.Register(&Command{
		Name: "cmd",
		Flags: []FlagDef{
			{Long: "dry-run", Short: "n", IsBool: true},
			{Long: "output", Short: "o"},
		},
		Run: func(c *RunContext) error {
			ctx = c
			return nil
		},
	})

	code := r.Run([]string{"cmd", "-n", "--output", "foo.txt", "arg1"})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, buf.String())
	}
	if !ctx.BoolFlag("dry-run") {
		t.Error("--dry-run not set")
	}
	if ctx.Flag("output") != "foo.txt" {
		t.Errorf("output = %q", ctx.Flag("output"))
	}
	if len(ctx.Args) != 1 || ctx.Args[0] != "arg1" {
		t.Errorf("args = %v", ctx.Args)
	}
}

func TestBundledShortFlags(t *testing.T) {
	var ctx *RunContext
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	r.Register(&Command{
		Name: "cmd",
		Flags: []FlagDef{
			{Long: "all", Short: "a", IsBool: true},
			{Long: "verbose", Short: "v", IsBool: true},
			{Long: "literal", Short: "l", IsBool: true},
		},
		Run: func(c *RunContext) error {
			ctx = c
			return nil
		},
	})

	code := r.Run([]string{"cmd", "-avl"})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !ctx.BoolFlag("all") || !ctx.BoolFlag("verbose") || !ctx.BoolFlag("literal") {
		t.Errorf("flags = %v", ctx.Flags)
	}
}

func TestUnknownFlag(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewRouter("test")
	r.Stderr = buf
	r.Register(&Command{
		Name: "cmd",
		Run:  func(c *RunContext) error { return nil },
	})

	code := r.Run([]string{"cmd", "--bogus"})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

func TestUnknownCommand(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewRouter("test")
	r.Stderr = buf
	r.Register(&Command{
		Name:  "kill",
		Short: "kill a thing",
		Run:   func(c *RunContext) error { return nil },
	})

	code := r.Run([]string{"kil"})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !bytes.Contains(buf.Bytes(), []byte("did you mean: test kill")) {
		t.Errorf("no did-you-mean in output: %s", buf.String())
	}
}

func TestHelpFlag(t *testing.T) {
	buf := &bytes.Buffer{}
	r := NewRouter("test")
	r.Stderr = buf
	r.Register(&Command{
		Name:  "cmd",
		Short: "do a thing",
		Long:  "Does the thing in detail.",
		Flags: []FlagDef{
			{Long: "verbose", Short: "v", IsBool: true, Description: "enable verbose"},
		},
		Run: func(c *RunContext) error { return nil },
	})

	code := r.Run([]string{"cmd", "--help"})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Contains(buf.Bytes(), []byte("--verbose")) {
		t.Errorf("help missing flag info: %s", buf.String())
	}
}

func TestPassRaw(t *testing.T) {
	var ctx *RunContext
	r := NewRouter("test")
	r.Stderr = &bytes.Buffer{}
	r.Register(&Command{
		Name:    "prompt",
		PassRaw: true,
		Run: func(c *RunContext) error {
			ctx = c
			return nil
		},
	})

	code := r.Run([]string{"prompt", "--", "hello", "world"})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(ctx.RawArgs) != 3 || ctx.RawArgs[0] != "--" {
		t.Errorf("raw args = %v", ctx.RawArgs)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kill", "kil", 1},
		{"list", "lst", 1},
		{"attend", "atend", 1},
		{"foo", "bar", 3},
	}
	for _, tc := range cases {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
