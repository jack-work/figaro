package cli

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/jack-work/figaro/internal/mark"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

type paintView uint8

const (
	viewTranscript paintView = iota
	viewPit
	viewSearch
	viewCommand
	viewComposer
	paintViews
)

var paintViewNames = [paintViews]string{
	viewTranscript: "transcript",
	viewPit:        "pit",
	viewSearch:     "search",
	viewCommand:    "command",
	viewComposer:   "composer",
}

var paintViewAttrs = func() (out [paintViews]otelmetric.MeasurementOption) {
	for v, name := range paintViewNames {
		out[v] = otelmetric.WithAttributeSet(attribute.NewSet(attribute.String("view", name)))
	}
	return out
}()

func (v paintView) String() string { return paintViewNames[v] }

const keysAwaitingMax = 256

type keysAwaitingPaint struct {
	at []time.Time
}

func (k *keysAwaitingPaint) arrived(at time.Time) {
	if len(k.at) < keysAwaitingMax {
		k.at = append(k.at, at)
	}
}

func (k *keysAwaitingPaint) painted(view paintView, at time.Time) {
	if len(k.at) == 0 {
		return
	}
	ctx := context.Background()
	for _, key := range k.at {
		d := at.Sub(key)
		figOtel.RecordKeyToPaint(ctx, d, paintViewAttrs[view])
		if mark.Enabled() {
			mark.Mark("key.paint", "view", view.String(), "ms", float64(d.Microseconds())/1000)
		}
	}
	figOtel.AddFramesSkipped(ctx, len(k.at)-1, paintViewAttrs[view])
	k.at = k.at[:0]
}

func (k *keysAwaitingPaint) forget() { k.at = k.at[:0] }

func (t *transcript) paintView() paintView {
	switch t.openPit() {
	case pitNothing:
		return viewTranscript
	case pitComposer:
		return viewComposer
	case pitSearch:
		return viewSearch
	case pitCommand:
		return viewCommand
	}
	return viewPit
}

func (t *transcript) answerKeys() {
	if t.awaiting && t.pit.shown == t.awaitShown {
		return
	}
	t.keys.painted(t.paintView(), time.Now())
}

func (t *transcript) owesKeys() bool { return t.dirty || t.awaiting && t.pit.shown == t.awaitShown }
