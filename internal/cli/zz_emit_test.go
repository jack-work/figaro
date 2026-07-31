package cli

import (
	"fmt"
	"os"
	"testing"
)

func TestEmitClipped(t *testing.T) {
	if os.Getenv("EMIT") == "" {
		t.Skip("set EMIT=1")
	}
	for _, c := range []struct{ name, s string }{
		{"charset", "\x1b(B abcdefghijklmnopqrstuvwxyz"},
		{"bare-esc", "\x1b abcdefghijklmnopqrstuvwxyz"},
		{"osc", "\x1b]0;title\x07 abcdefghijklmnopqrstuvwxyz"},
		{"plain", "abcdefghijklmnopqrstuvwxyz"},
	} {
		fmt.Printf("%s|%s|END\n", c.name, clipToWidth(c.s, 10))
	}
}
