package cli

import (
	"errors"
	"strings"
	"testing"
)

// A host app passes the message on stdin so it never shows in the process
// table; a prompt in argv beside --stdin is refused, never preferred.
func TestStdinPromptNeverComesFromArgv(t *testing.T) {
	opts, rest, err := extractSendFlags([]string{"--id", "abc", "-r", "--stdin"})
	if err != nil || !opts.stdin {
		t.Fatalf("parse: %+v %v", opts, err)
	}
	got, err := promptFromStdin(opts, rest, strings.NewReader("hello from stdin\n"))
	if err != nil || got != "hello from stdin" {
		t.Fatalf("prompt = %q, %v", got, err)
	}

	opts, rest, err = extractSendFlags([]string{"--stdin", "--", "secret in argv"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := promptFromStdin(opts, rest, strings.NewReader("from stdin")); !errors.Is(err, errStdinAndArgv) {
		t.Fatalf("--stdin with a prompt in argv: %q, %v", got, err)
	}
	if _, err := promptFromStdin(opts, []string{"--"}, strings.NewReader("x")); !errors.Is(err, errStdinAndArgv) {
		t.Fatalf("--stdin with a bare boundary: %v", err)
	}
	if _, err := promptFromStdin(sendOpts{stdin: true}, nil, strings.NewReader(" \n")); err == nil {
		t.Fatal("an empty stdin was taken as a prompt")
	}
}

func TestWithoutStdinThePromptStaysInArgv(t *testing.T) {
	opts, rest, err := extractSendFlags([]string{"--", "hi", "there"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := promptFromStdin(opts, rest, strings.NewReader("ignored"))
	if err != nil || got != "hi there" {
		t.Fatalf("prompt = %q, %v", got, err)
	}
}

func TestStdinIsRefusedWhereNoShellStdinExists(t *testing.T) {
	if _, err := planFork([]string{"abc", "--stdin"}); err == nil {
		t.Error("fork took --stdin")
	}
	if _, err := planSend([]string{"--stdin"}); err == nil {
		t.Error("the pager's send took --stdin")
	}
	if err := validateSendOpts(sendOpts{stdin: true, exec: true}, false); err == nil {
		t.Error("--stdin with --exec")
	}
}
