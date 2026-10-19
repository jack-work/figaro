package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/keyconf"
)

const nameTestID = "3fa2c19b0d7e4f6a8b1c2d3e4f5a6b7c"

func withShownIDs(t *testing.T, style idStyle) {
	t.Helper()
	prev := shownIDs
	shownIDs = style
	t.Cleanup(func() { shownIDs = prev })
}

// The setting changes what a person reads; -j fields keep the handle either way.
func TestShownIDs_ShortHexChangesNamesNotHandles(t *testing.T) {
	withShownIDs(t, idsAsShortHex)
	handle := ariaHandle(nameTestID)
	if got := ariaName(nameTestID); got != "3fa2c" {
		t.Fatalf("ariaName = %q, want the five-digit prefix", got)
	}
	if got := ariaName("@" + nameTestID); got != "@3fa2c" {
		t.Fatalf("a form keeps its sigil: %q", got)
	}
	if got := ariaName("aria0001"); got != "aria0001" {
		t.Fatalf("a non-hex id passes through: %q", got)
	}
	f := &rpc.FigaroInfoResponse{ID: nameTestID, Short: "3fa2c1", Handle: handle}
	if got := rowName(f); got != "3fa2c1" {
		t.Fatalf("rowName prefers the daemon's unique short: %q", got)
	}
	if got := senderLabel(rpc.AriaLabelPrefix+nameTestID, handle); got != "figaro 3fa2c" {
		t.Fatalf("senderLabel = %q", got)
	}
	if ariaHandle(nameTestID) != handle {
		t.Fatal("ariaHandle must not follow the setting")
	}
}

func TestShownIDs_HandlesByDefault(t *testing.T) {
	withShownIDs(t, idsAsHandles)
	if err := applyDisplay(keyconf.Display{}); err != nil || shownIDs != idsAsHandles {
		t.Fatalf("an empty display table means handles: %v %v", shownIDs, err)
	}
	f := &rpc.FigaroInfoResponse{ID: nameTestID, Short: "3fa2c1", Handle: "some-thing-else"}
	if got := rowName(f); got != "some-thing-else" {
		t.Fatalf("rowName = %q, want the daemon's handle", got)
	}
	if err := applyDisplay(keyconf.Display{IDs: "short"}); err != nil || shownIDs != idsAsShortHex {
		t.Fatalf("ids = short: %v %v", shownIDs, err)
	}
	if err := applyDisplay(keyconf.Display{IDs: "hex"}); err == nil || shownIDs != idsAsHandles {
		t.Fatalf("an unknown value is refused and falls back to handles: %v %v", shownIDs, err)
	}
}
