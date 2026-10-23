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
	RetainForms(ctx context.Context, dryRun bool, pace time.Duration) ([]store.FormRetention, error)
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
		a.retainForms(ctx, false)
		timer.Reset(retentionEvery)
	}
}

type retentionPass struct {
	Mode   config.Retention
	DryRun bool
	Forms  []store.FormRetention
	Err    error
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
	pass.Forms, pass.Err = r.RetainForms(ctx, pass.DryRun, retentionPace)
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
