package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDormancyDefaults(t *testing.T) {
	var l *Loaded // nil must be as safe as empty: a store opened without config
	require.Equal(t, 15*time.Minute, l.DormantAfter())
	require.Equal(t, 2*time.Minute, l.SweepInterval())

	empty := &Loaded{}
	require.Equal(t, 15*time.Minute, empty.DormantAfter())
	require.Equal(t, 2*time.Minute, empty.SweepInterval())
}

func TestDormancyConfigured(t *testing.T) {
	mins, secs := 45, 30
	l := &Loaded{Config: Config{Memory: MemoryConfig{
		DormantAfterMinutes:  &mins,
		SweepIntervalSeconds: &secs,
	}}}
	require.Equal(t, 45*time.Minute, l.DormantAfter())
	require.Equal(t, 30*time.Second, l.SweepInterval())
}

// Zero disables reclamation; every caller reads 0 as "never". Negative is
// the same request written carelessly and must not become a huge duration.
func TestDormancyDisabled(t *testing.T) {
	for _, mins := range []int{0, -1} {
		l := &Loaded{Config: Config{Memory: MemoryConfig{DormantAfterMinutes: &mins}}}
		require.Zero(t, l.DormantAfter(), "dormant_after_minutes=%d", mins)
	}
}

// A sweep interval below a second would spin the ticker, so it is floored
// rather than honoured.
func TestSweepIntervalFloored(t *testing.T) {
	for _, secs := range []int{0, -5} {
		l := &Loaded{Config: Config{Memory: MemoryConfig{SweepIntervalSeconds: &secs}}}
		require.Equal(t, time.Second, l.SweepInterval(), "sweep_interval_seconds=%d", secs)
	}
}
