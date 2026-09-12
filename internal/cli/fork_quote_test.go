package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/jkrpc"
)

// The pre-flight needs no daemon for an LT cut: both numbers are in hand.
func TestForkQuotePreflightAgainstAnLTCut(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		cut    uint64
		prompt string
		refuse bool
	}{
		{400, "<412.0:0-5>! what", true},
		{412, "<412>! what", true},
		{413, "<412>! what", false},
		{400, "<300.0:0-412.0:8>! what", true}, // a span ending past the cut
		{400, "<300.0:0-399.0:8>! what", false},
		{400, "no quote here", false},
		{400, "<412.0:0-5> unterminated is text", false},
	}
	for _, c := range cases {
		err := forkQuotePreflight(ctx, nil, "aria", forkPoint{lt: c.cut}, c.prompt)
		if c.refuse && (err == nil || !strings.Contains(err.Error(), "would not contain it")) {
			t.Fatalf("cut %d %q: got %v, want a refusal", c.cut, c.prompt, err)
		}
		if !c.refuse && err != nil && strings.Contains(err.Error(), "would not contain it") {
			t.Fatalf("cut %d %q: refused: %v", c.cut, c.prompt, err)
		}
	}
	if err := forkQuotePreflight(ctx, nil, "aria", forkPoint{}, "<412>! x"); err != nil {
		t.Fatalf("a head fork shares everything: %v", err)
	}
}

func TestWireErrorTextDropsTheCode(t *testing.T) {
	inner := &jkrpc.Error{Code: -32000, Message: "quote: <7.9>: lt 7 has 2 blocks, not block 9"}
	err := fmt.Errorf("send: %w", inner)
	if got := wireErrorText(err); got != "send: quote: <7.9>: lt 7 has 2 blocks, not block 9" {
		t.Fatalf("got %q", got)
	}
	if got := wireErrorText(errors.New("plain")); got != "plain" {
		t.Fatalf("got %q", got)
	}
}
