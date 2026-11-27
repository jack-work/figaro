package angelus

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/config"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/store"
)

type formRetainer interface {
	RetainFormsCounted(ctx context.Context, dryRun bool, pace time.Duration) ([]store.FormRetention, int, error)
}

type queueSweeper interface {
	SweepQueueEntries(now time.Time, grace time.Duration) (int, error)
}

const (
	retentionFirst = 5 * time.Minute
	retentionEvery = time.Hour
	retentionPace  = 20 * time.Millisecond
)

func (a *Angelus) retention(ctx context.Context) {
	if _, ok := a.Backend.(formRetainer); !ok {
		return
	}
	timer := time.NewTimer(retentionFirst)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		a.runRetentionPass(ctx)
		timer.Reset(retentionEvery)
	}
}

func (a *Angelus) runRetentionPass(ctx context.Context) {
	start := time.Now()
	pass := a.retainForms(ctx, false)
	entries := a.sweepQueueEntries()
	logRetentionPass(pass, entries, time.Since(start))
}

type retentionPass struct {
	Mode    config.Retention
	DryRun  bool
	Forms   []store.FormRetention
	Scanned int
	Err     error
}

func logRetentionPass(pass retentionPass, entries int, took time.Duration) {
	segments := 0
	for _, f := range pass.Forms {
		segments += f.Dropped
	}
	slog.Info("retention pass", "mode", string(pass.Mode), "dry_run", pass.DryRun, "forms_scanned", pass.Scanned, "segments_dropped", segments, "queue_entries_deleted", entries, "took", took)
}

func (a *Angelus) retainForms(ctx context.Context, planOnly bool) retentionPass {
	mode, err := a.Settings.Retention()
	if err != nil {
		slog.Warn("form retention off", "err", err)
	}
	pass := retentionPass{Mode: mode, DryRun: planOnly || mode != config.RetentionOn, Err: err}
	r, ok := a.Backend.(formRetainer)
	if !ok || mode == config.RetentionOff {
		return pass
	}
	start := time.Now()
	pass.Forms, pass.Scanned, pass.Err = r.RetainFormsCounted(ctx, pass.DryRun, retentionPace)
	took := time.Since(start)
	figOtel.RecordSweep(ctx, took, attribute.String("phase", "retain_forms"))
	var segments int
	var bytes int64
	for _, f := range pass.Forms {
		segments += f.Dropped
		bytes += f.Bytes
	}
	if pass.Err != nil {
		slog.Warn("form retention", "err", pass.Err)
	}
	if len(pass.Forms) == 0 {
		slog.Debug("form retention: nothing to drop", "took", took)
		return pass
	}
	msg := "form history dropped"
	if pass.DryRun {
		msg = "form history droppable (dry run)"
	}
	slog.Info(msg, "forms", len(pass.Forms), "segments", segments, "bytes", bytes, "took", took)
	return pass
}

func (a *Angelus) sweepQueueEntries() int {
	q, ok := a.Backend.(queueSweeper)
	if !ok {
		return 0
	}
	n, err := q.SweepQueueEntries(time.Now(), a.Settings.QueueEntryGrace())
	if err != nil {
		slog.Warn("queue entry sweep", "err", err)
	}
	if n > 0 {
		slog.Info("finished queue entries deleted", "entries", n)
	}
	return n
}
