package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const FirstByteTimeoutKey = "system.first_byte_timeout"

const DefaultFirstByteTimeout = 90 * time.Second

func FirstByteTimeout(snapshot form.Snapshot) time.Duration {
	raw, ok := snapshot.Get(FirstByteTimeoutKey)
	if !ok {
		return DefaultFirstByteTimeout
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return DefaultFirstByteTimeout
	}
	switch s {
	case "0", "off":
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return DefaultFirstByteTimeout
	}
	return d
}

type FirstByteTimeoutError struct{ After time.Duration }

func (e *FirstByteTimeoutError) Error() string {
	return fmt.Sprintf("no byte arrived within %s; %s raises it", e.After, FirstByteTimeoutKey)
}

type firstByteKey struct{}

type firstByte struct {
	after  time.Duration
	cancel context.CancelCauseFunc
	seen   atomic.Bool
	mu     sync.Mutex
	timer  *time.Timer
}

func (fb *firstByte) arm() {
	if fb.seen.Load() {
		return
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.timer != nil {
		fb.timer.Stop()
	}
	fb.timer = time.AfterFunc(fb.after, func() {
		if !fb.seen.Load() {
			fb.cancel(&FirstByteTimeoutError{After: fb.after})
		}
	})
}

func (fb *firstByte) pause() {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.timer != nil {
		fb.timer.Stop()
		fb.timer = nil
	}
}

func WatchFirstByte(ctx context.Context, d time.Duration) (context.Context, func(error) error) {
	if d <= 0 {
		return ctx, func(err error) error { return err }
	}
	reqCtx, cancel := context.WithCancelCause(ctx)
	fb := &firstByte{after: d, cancel: cancel}
	reqCtx = context.WithValue(reqCtx, firstByteKey{}, fb)
	reqCtx = httptrace.WithClientTrace(reqCtx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { fb.arm() },
	})
	return reqCtx, func(err error) error {
		fb.pause()
		cause := context.Cause(reqCtx)
		cancel(nil)
		var late *FirstByteTimeoutError
		if err != nil && ctx.Err() == nil && errors.As(cause, &late) {
			return &StreamCut{Awaited: "a first byte", Err: late}
		}
		return err
	}
}

func SawByte(ctx context.Context) {
	if fb, ok := ctx.Value(firstByteKey{}).(*firstByte); ok && !fb.seen.Load() {
		fb.seen.Store(true)
		fb.pause()
	}
}

func PauseFirstByte(ctx context.Context) {
	if fb, ok := ctx.Value(firstByteKey{}).(*firstByte); ok {
		fb.pause()
	}
}
