package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type firstByte struct{ seen atomic.Bool }

func WatchFirstByte(ctx context.Context, d time.Duration) (context.Context, func(error) error) {
	if d <= 0 {
		return ctx, func(err error) error { return err }
	}
	fb := &firstByte{}
	reqCtx, cancel := context.WithCancelCause(context.WithValue(ctx, firstByteKey{}, fb))
	timer := time.AfterFunc(d, func() {
		if !fb.seen.Load() {
			cancel(&FirstByteTimeoutError{After: d})
		}
	})
	return reqCtx, func(err error) error {
		timer.Stop()
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
	}
}
