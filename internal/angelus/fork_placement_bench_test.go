package angelus_test

import (
	"log/slog"
	"testing"
)

// The fork RPC end to end. cut=ancestor forks a successor inside its first
// holder's turns, the path that moves the new node under the aria forked;
// cut=own forks the first holder in its own turns and moves nothing.
func BenchmarkForkPlacement(b *testing.B) {
	defer func(l *slog.Logger) { slog.SetDefault(l) }(slog.Default())
	slog.SetDefault(slog.New(slog.DiscardHandler))
	for _, shape := range []string{"cut=ancestor", "cut=own"} {
		b.Run(shape, func(b *testing.B) {
			a, acli, ctx := daemonFixture(b)
			first := threeTurnAria(b, a, acli, ctx)
			target := first
			if shape == "cut=ancestor" {
				successor, err := acli.Fork(ctx, first, 3, 0, nil, nil)
				if err != nil {
					b.Fatal(err)
				}
				target = successor.Alternative
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := acli.Fork(ctx, target, 1, 0, nil, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
