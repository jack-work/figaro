package cli

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/jack-work/figaro/sdk"
)

type followed struct {
	ctx    context.Context
	cancel context.CancelFunc
	ep     sdk.Addr
	handle sdk.NotifyHandler
	again  func(*sdk.Aria)
	cur    atomic.Pointer[sdk.Aria]
	done   chan struct{}
}

func follow(ctx context.Context, ep sdk.Addr, handle sdk.NotifyHandler, again func(*sdk.Aria)) (*followed, error) {
	a, err := sdk.DialAria(ep, handle)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	f := &followed{ctx: ctx, cancel: cancel, ep: ep, handle: handle, again: again, done: make(chan struct{})}
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
		if !a.Lapsed() || f.ctx.Err() != nil {
			return
		}
		next, err := sdk.DialAria(f.ep, f.handle)
		if err != nil {
			slog.Warn("the door ended the follow at its age limit and following again failed", "aria", f.ep.ID, "err", err)
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

func (f *followed) aria() *sdk.Aria { return f.cur.Load() }

func (f *followed) Done() <-chan struct{} { return f.done }

func (f *followed) Close() {
	f.cancel()
	<-f.done
}
