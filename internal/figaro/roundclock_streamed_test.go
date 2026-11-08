package figaro

import (
	"testing"
	"time"
)

// round.streamed is false when a round of at least 32 output tokens spent
// less than 1 ms per 10 of them (10k tokens/s, past any real decode) between
// its first and last output delta.
func TestStreamedRoundRule(t *testing.T) {
	for _, c := range []struct {
		tokens int
		span   time.Duration
		want   bool
	}{
		{31, 0, true},
		{32, 0, false},
		{300, 29 * time.Millisecond, false},
		{300, 30 * time.Millisecond, true},
		{300, 5 * time.Second, true},
		{1000, 99 * time.Millisecond, false},
		{1000, 100 * time.Millisecond, true},
	} {
		if got := streamedRound(c.tokens, c.span); got != c.want {
			t.Errorf("streamedRound(%d tokens, %s) = %t, want %t", c.tokens, c.span, got, c.want)
		}
	}
}
