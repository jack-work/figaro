package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

var ErrStalled = errors.New("provider connection stalled")

type StallError struct {
	Window time.Duration
}

func (e *StallError) Error() string {
	return fmt.Sprintf("%v: no bytes for %s", ErrStalled, e.Window)
}

func (e *StallError) Is(target error) bool { return target == ErrStalled }

type clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) timer
}

type timer interface {
	Reset(d time.Duration) bool
	Stop() bool
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) AfterFunc(d time.Duration, f func()) timer { return time.AfterFunc(d, f) }

type watchdog struct {
	window time.Duration
	clock  clock
	origin time.Time
	last   atomic.Int64
	timer  timer
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func (t *Transport) watched(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	w := &watchdog{window: t.stall, clock: t.clock, origin: t.clock.Now(), ctx: ctx, cancel: cancel}
	w.timer = t.clock.AfterFunc(time.Hour, w.fire)
	w.timer.Reset(w.window)

	req = req.WithContext(ctx)
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &progressBody{ReadCloser: req.Body, w: w}
	}
	if getBody := req.GetBody; getBody != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			b, err := getBody()
			if err != nil || b == nil || b == http.NoBody {
				return b, err
			}
			return &progressBody{ReadCloser: b, w: w}, nil
		}
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		err = w.verdict(err)
		w.done()
		return nil, err
	}
	w.kick()
	resp.Body = &stallBody{ReadCloser: resp.Body, w: w}
	return resp, nil
}

func (w *watchdog) elapsed() time.Duration { return w.clock.Now().Sub(w.origin) }

func (w *watchdog) kick() { w.last.Store(int64(w.elapsed())) }

func (w *watchdog) fire() {
	idle := w.elapsed() - time.Duration(w.last.Load())
	if idle < w.window {
		w.timer.Reset(w.window - idle)
		return
	}
	w.cancel(&StallError{Window: w.window})
}

func (w *watchdog) verdict(err error) error {
	var stall *StallError
	if errors.As(context.Cause(w.ctx), &stall) {
		return fmt.Errorf("%w (%v)", stall, err)
	}
	return err
}

func (w *watchdog) done() {
	w.timer.Stop()
	w.cancel(context.Canceled)
}

type progressBody struct {
	io.ReadCloser
	w *watchdog
}

func (b *progressBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.w.kick()
	}
	return n, err
}

type stallBody struct {
	io.ReadCloser
	w *watchdog
}

func (b *stallBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.w.kick()
	}
	switch {
	case err == io.EOF:
		b.w.timer.Stop()
	case err != nil:
		err = b.w.verdict(err)
	}
	return n, err
}

func (b *stallBody) Close() error {
	err := b.ReadCloser.Close()
	b.w.done()
	return err
}
