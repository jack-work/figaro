package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKillRequestFlags(t *testing.T) {
	cases := []struct {
		name    string
		now     bool
		when    string
		wantNow bool
		grace   time.Duration
		err     string
	}{
		{name: "no flags take the daemon default"},
		{name: "--now deletes at once", now: true, wantNow: true},
		{name: "--when 90m", when: "90m", grace: 90 * time.Minute},
		{name: "--when 6h", when: "6h", grace: 6 * time.Hour},
		{name: "--when 2d", when: "2d", grace: 48 * time.Hour},
		{name: "--when 1w", when: "1w", grace: 7 * 24 * time.Hour},
		{name: "--when 0 is --now", when: "0", wantNow: true},
		{name: "--now with --when is refused", now: true, when: "1h", err: "exclusive"},
		{name: "a negative grace is refused", when: "-1h", err: "negative"},
		{name: "garbage is refused", when: "soon", err: "--when"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := killRequest("abc", true, c.now, c.when)
			if c.err != "" {
				require.ErrorContains(t, err, c.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "abc", req.FigaroID)
			require.True(t, req.Recursive)
			require.Equal(t, c.wantNow, req.Now)
			require.Equal(t, c.grace.Milliseconds(), req.GraceMS)
		})
	}
}
