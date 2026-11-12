package cli

import (
	"context"
	"testing"
	"time"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

// Every command flushes telemetry on its way out. Against a collector that
// accepts and never answers, that flush is the whole of the command's extra
// wall time, so it gets ExitFlush and runs once however many exit paths fire.
func TestACommandsExitFlushIsBoundedByTheExitFlushBudget(t *testing.T) {
	var budgets []time.Duration
	wedged := func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("the exit flush was given no deadline")
			return nil
		}
		budgets = append(budgets, time.Until(deadline))
		<-ctx.Done()
		return ctx.Err()
	}

	flush := telemetryFlush(context.Background(), wedged)
	start := time.Now()
	flush()
	took := time.Since(start)
	flush()

	if len(budgets) != 1 {
		t.Fatalf("the flush ran %d times, want once", len(budgets))
	}
	if budgets[0] > figOtel.ExitFlush || budgets[0] < figOtel.ExitFlush/2 {
		t.Fatalf("the flush was given %v, want %v", budgets[0], figOtel.ExitFlush)
	}
	if took > figOtel.ExitFlush+2*time.Second {
		t.Fatalf("a wedged flush held the command for %v, want about %v", took, figOtel.ExitFlush)
	}
}
