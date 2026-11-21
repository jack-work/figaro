package cli

import (
	"strings"
	"testing"
)

func TestNewLogDressesTheBirthAsALog(t *testing.T) {
	opts, rest, err := extractPromptFlags([]string{"--log", "-S", "mantra=ledger", "--", "first entry"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.log || !opts.outfit.mintsLog() {
		t.Fatalf("--log did not dress a log birth: %+v", opts.outfit)
	}
	if len(rest) != 2 || rest[1] != "first entry" {
		t.Fatalf("rest %v", rest)
	}
	if err := validateNewOpts(opts); err != nil {
		t.Fatalf("new --log: %v", err)
	}
	plain, _, err := extractPromptFlags([]string{"--", "hi"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if plain.outfit.mintsLog() {
		t.Fatal("a plain new minted a log")
	}
}

func TestNewLogRefusesCast(t *testing.T) {
	opts, _, err := extractPromptFlags([]string{"--log", "-C"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateNewOpts(opts); err == nil || !strings.Contains(err.Error(), "cast") {
		t.Fatalf("new --log -C = %v, want a refusal naming cast", err)
	}
}
