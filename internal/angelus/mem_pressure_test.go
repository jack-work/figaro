package angelus

import (
	"math"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/store"
)

// The warning has to fire before the runtime is pinned at the limit, and never
// when no limit is set.
func TestUnderPressureNeedsALimitAndAThreshold(t *testing.T) {
	require.False(t, underPressure(900, 0, memPressureHigh))
	require.False(t, underPressure(900, math.MaxInt64, memPressureHigh))
	require.False(t, underPressure(800, 1000, memPressureHigh))
	require.True(t, underPressure(850, 1000, memPressureHigh))
	require.True(t, underPressure(990, 1000, memPressureHigh))
}

func TestMemStatusReportsTheLiveHeapAndTheForms(t *testing.T) {
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = be.Close() })
	a := New(Config{RuntimeDir: t.TempDir(), Backend: be})
	runtime.GC()

	st := a.MemStatus()
	require.Positive(t, st.HeapLiveBytes, "live heap from runtime/metrics unread: %+v", st)
	require.False(t, st.MemPressure, "a fresh test process is not at its limit")
	require.Zero(t, st.ResidentForms, "no form opened yet")
}
