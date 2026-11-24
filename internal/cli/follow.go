package cli

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

type followed struct {
	ctx     context.Context
	cancel  context.CancelFunc
	ep      sdk.Addr
	handle  sdk.NotifyHandler
	again   func(*sdk.Aria)
	persist bool
	cur     atomic.Pointer[sdk.Aria]
	done    chan struct{}
}

func follow(ctx context.Context, ep sdk.Addr, handle sdk.NotifyHandler, again func(*sdk.Aria)) (*followed, error) {
	return dialFollow(ctx, ep, handle, again, false)
}

func followAcrossRestarts(ctx context.Context, ep sdk.Addr, handle sdk.NotifyHandler, again func(*sdk.Aria)) (*followed, error) {
	return dialFollow(ctx, ep, handle, again, true)
}

func dialFollow(ctx context.Context, ep sdk.Addr, handle sdk.NotifyHandler, again func(*sdk.Aria), persist bool) (*followed, error) {
	a, err := sdk.DialAria(ep, handle)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	f := &followed{ctx: ctx, cancel: cancel, ep: ep, handle: handle, again: again, persist: persist, done: make(chan struct{})}
	f.cur.Store(a)
	go f.watch(a)
	return f, nil
}

func (f *followed) watch(a *sdk.Aria) {
	defer close(f.done)
	for {
		select {
		case <-a.Done():
		case <-f.ctx.Done():
			a.Close()
			return
		}
		if f.ctx.Err() != nil {
			return
		}
		var next *sdk.Aria
		switch {
		case a.Lapsed():
			var err error
			if next, err = sdk.DialAria(f.ep, f.handle); err != nil {
				slog.Warn("the door ended the follow at its age limit and following again failed", "aria", f.ep.ID, "err", err)
				return
			}
		case f.persist:
			if next = f.redial(); next == nil {
				return
			}
		default:
			return
		}
		if f.ctx.Err() != nil {
			next.Close()
			return
		}
		f.cur.Store(next)
		if f.again != nil {
			f.again(next)
		}
		a = next
	}
}

func (f *followed) redial() *sdk.Aria {
	wait := reconnectFirst
	for {
		select {
		case <-f.ctx.Done():
			return nil
		case <-time.After(wait):
		}
		next, err := sdk.DialAria(f.ep, f.handle)
		if err == nil {
			return next
		}
		var answered *jkrpc.Error
		if errors.As(err, &answered) {
			slog.Warn("the daemon refused to follow again", "aria", f.ep.ID, "err", err)
			return nil
		}
		wait = nextReconnectWait(wait, false)
	}
}

func (f *followed) aria() *sdk.Aria { return f.cur.Load() }

func (f *followed) Done() <-chan struct{} { return f.done }

func (f *followed) Close() {
	f.cancel()
	<-f.done
}
