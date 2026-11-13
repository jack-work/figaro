package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
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
			req, err := killRequest("abc", true, false, c.now, c.when)
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

func TestKillRequestRefusesRWithKeepChildren(t *testing.T) {
	_, err := killRequest("abc", true, true, false, "")
	require.ErrorContains(t, err, "--keep-children")
	req, err := killRequest("abc", false, true, true, "")
	require.NoError(t, err)
	require.True(t, req.KeepChildren)
	require.True(t, req.Now)
}

func TestPagerKillTakesKeepChildren(t *testing.T) {
	spec, req, err := parseKillArgs([]string{"--keep-children", "abc"})
	require.NoError(t, err)
	require.Equal(t, "abc", spec)
	require.True(t, req.KeepChildren)
	_, _, err = parseKillArgs([]string{"-r", "--keep-children", "abc"})
	require.ErrorContains(t, err, "--keep-children")
}

func TestKillSaysHowManyWentWithIt(t *testing.T) {
	now := time.Now()
	require.Equal(t, "killed P", killedLine("P", rpc.KillResponse{Removed: []string{"p"}}, now))
	require.Equal(t, "killed P and 1 under it", killedLine("P", rpc.KillResponse{Removed: []string{"p", "c"}}, now))
	require.Equal(t, "killed P and 2 under it; purged in 60m (figaro reaped restore P, or kill --now)",
		killedLine("P", rpc.KillResponse{Reaped: []string{"p", "c", "g"}, PurgeAt: now.Add(time.Hour).UnixMilli()}, now))
}
