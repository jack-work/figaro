package cli

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

const modelsWantEvery = time.Minute

type modelsFeed struct {
	angelus  func() (*sdk.Angelus, error)
	ask      func(provider, model string)
	onChange func()
	mirror   formMirror

	mu      sync.Mutex
	conn    *sdk.Aria
	dialing bool
	wanted  map[string]time.Time
}

func newModelsFeed(angelus func() (*sdk.Angelus, error), onChange func()) *modelsFeed {
	f := &modelsFeed{angelus: angelus, onChange: onChange, wanted: map[string]time.Time{}}
	f.ask = func(provider, model string) {
		acli, err := angelus()
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		_ = acli.ModelsWant(ctx, provider, model)
	}
	return f
}

func (f *modelsFeed) snapshot() form.Snapshot {
	if f == nil {
		return form.Snapshot{}
	}
	snap, _, _ := f.mirror.state()
	return snap
}

func (f *modelsFeed) ensure() {
	if f == nil || f.angelus == nil {
		return
	}
	f.mu.Lock()
	if f.conn != nil || f.dialing {
		f.mu.Unlock()
		return
	}
	f.dialing = true
	f.mu.Unlock()
	go f.dial()
}

func (f *modelsFeed) dial() {
	defer func() {
		f.mu.Lock()
		f.dialing = false
		f.mu.Unlock()
	}()
	acli, err := f.angelus()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	att, err := acli.Attach(ctx, models.FormID)
	if err != nil {
		return
	}
	conn, err := sdk.DialAria(transport.Endpoint{Scheme: att.Endpoint.Scheme, Address: att.Endpoint.Address}, f.handle)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conn = conn
	f.mu.Unlock()
	go func() {
		<-conn.Done()
		f.mu.Lock()
		if f.conn == conn {
			f.conn = nil
		}
		f.mu.Unlock()
	}()
	seed, err := conn.Form(ctx)
	if err != nil {
		return
	}
	f.mirror.reset(seed.Snapshot, seed.Version)
	f.changed()
}

func (f *modelsFeed) handle(method string, params json.RawMessage) {
	if method != rpc.MethodFormDelta {
		return
	}
	var d rpc.FormDelta
	if json.Unmarshal(params, &d) != nil || d.AriaID != models.FormID {
		return
	}
	switch f.mirror.apply(d) {
	case formResync:
		go f.resync()
		return
	case formIncompatible:
		return
	}
	f.changed()
}

func (f *modelsFeed) resync() {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return
	}
	_, version, _ := f.mirror.state()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	resp, err := conn.FormSince(ctx, version)
	if err != nil {
		return
	}
	f.mirror.catchUp(resp)
	f.changed()
}

func (f *modelsFeed) want(provider, model string) {
	if f == nil || f.ask == nil {
		return
	}
	key := provider + "\x00" + model
	now := time.Now()
	f.mu.Lock()
	if at, ok := f.wanted[key]; ok && now.Sub(at) < modelsWantEvery {
		f.mu.Unlock()
		return
	}
	f.wanted[key] = now
	f.mu.Unlock()
	go f.ask(provider, model)
}

func (f *modelsFeed) changed() {
	if f.onChange != nil {
		f.onChange()
	}
}

func (f *modelsFeed) close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	conn := f.conn
	f.conn = nil
	f.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}
