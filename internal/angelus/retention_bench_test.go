package angelus_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// GC must not take the door down: round trips while a retention pass drops
// form history, compared with an idle daemon.
func BenchmarkDoorRoundTripWhileRetaining(b *testing.B) {
	for _, retaining := range []bool{false, true} {
		name := "idle"
		if retaining {
			name = "retaining"
		}
		b.Run(name, func(b *testing.B) {
			a, acli, ctx, _ := daemonFixtureConfig(b, "[store]\nsegment_size = 1048576\ngc = \"on\"\n")
			target, err := acli.Create(ctx, dress(b, "mock"), nil)
			require.NoError(b, err)
			conn := dialAria(b, a, target.FigaroID, nil)
			for i := 0; i < 10; i++ {
				longFormTB(b, a, acli, ctx)
			}
			var wg sync.WaitGroup
			if retaining {
				wg.Add(1)
				go func() { defer wg.Done(); a.RetainFormsForBench(context.Background()) }()
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
