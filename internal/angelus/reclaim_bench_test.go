package angelus_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/sdk"
)

func spentArias(b *testing.B, a *angelus.Angelus, acli *sdk.Angelus, ctx context.Context, n int) {
	b.Helper()
	for i := 0; i < n; i++ {
		c, err := acli.Create(ctx, dress(b, "mock"), nil)
		require.NoError(b, err)
		spendTB(b, a, c.FigaroID)
	}
	time.Sleep(20 * time.Millisecond)
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*p)]
}

// The sweep loop also reaps dead pids and sweeps cache budgets; what it spends
// on lifetimes is what those wait for. Enqueue is the lane's cost; drain is
// what the sweep goroutine used to pay inline.
func BenchmarkLifetimeSweepWith200Due(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a, acli, ctx := hardDeleteFixture(b)
		waitRolesReadyTB(b, a)
		spentArias(b, a, acli, ctx, 200)
		b.StartTimer()
		start := time.Now()
		a.ExpireTTLForBench()
		enqueue := time.Since(start)
		b.StopTimer()
		start = time.Now()
		a.WaitReclaimsForBench()
		drain := time.Since(start)
		b.ReportMetric(float64(enqueue.Microseconds()), "sweep-us")
		b.ReportMetric(float64(drain.Milliseconds()), "removals-ms")
	}
}

// GC must not take the door down: round trips while 200 removals run against
// the same store, compared with an idle daemon.
func BenchmarkDoorRoundTripWhileReclaiming(b *testing.B) {
	for _, reclaiming := range []bool{false, true} {
		name := "idle"
		if reclaiming {
			name = "reclaiming"
		}
		b.Run(name, func(b *testing.B) {
			a, acli, ctx := hardDeleteFixture(b)
			waitRolesReadyTB(b, a)
			target, err := acli.Create(ctx, dress(b, "mock"), nil)
			require.NoError(b, err)
			conn := dialAria(b, a, target.FigaroID, nil)
			if reclaiming {
				spentArias(b, a, acli, ctx, 200)
			}
			var wg sync.WaitGroup
			if reclaiming {
				wg.Add(1)
				go func() { defer wg.Done(); a.ExpireTTLForBench(); a.WaitPacedReclaimsForBench() }()
			}
			lat := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				var out map[string]any
				require.NoError(b, conn.Call(ctx, "figaro.context", struct{}{}, &out))
				lat = append(lat, time.Since(start))
			}
			b.StopTimer()
			wg.Wait()
			b.ReportMetric(float64(percentile(lat, 0.5).Microseconds()), "p50-us")
			b.ReportMetric(float64(percentile(lat, 0.99).Microseconds()), "p99-us")
			b.ReportMetric(float64(percentile(lat, 1).Microseconds()), "max-us")
		})
	}
}
