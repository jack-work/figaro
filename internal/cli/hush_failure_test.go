package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/hush/managed"
)

// On 2026-11-04 every figaro command hung on a Secret Service that had run out
// of file descriptors. Once hush gives up on it, the first thing the user is
// pointed at must be the command that shows what the keyring is doing.
func TestAnUnresponsiveKeyringPointsAtVaultStatusFirst(t *testing.T) {
	err := fmt.Errorf("unlock figaro: %w, and asking instead failed: %w",
		fmt.Errorf("%w within 5s", managed.ErrKeyringUnresponsive),
		errors.New("stdin is not a terminal"))
	var out bytes.Buffer
	if !explainHushFailure(&out, err) {
		t.Fatalf("an unresponsive keyring was not explained:\n%s", out.String())
	}
	var advice []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "  ") {
			advice = append(advice, strings.TrimSpace(line))
		}
	}
	if len(advice) == 0 || !strings.HasPrefix(advice[0], "figaro vault status") {
		t.Fatalf("first suggestion is %q, want figaro vault status:\n%s", advice, out.String())
	}
}

func TestAnUnexplainedHushFailureIsLeftToTheCaller(t *testing.T) {
	var out bytes.Buffer
	if explainHushFailure(&out, errors.New("agent socket refused")) {
		t.Fatalf("an unrelated failure was explained:\n%s", out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q for an unrelated failure", out.String())
	}
}
