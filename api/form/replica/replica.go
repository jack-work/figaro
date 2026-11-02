package replica

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

type Transport interface {
	Set(ctx context.Context, req rpc.SetRequest) (*rpc.SetResponse, error)
	Load(ctx context.Context, client string) (*rpc.FormResponse, error)
}

type Options struct {
	Debounce, MaxWait time.Duration
	Timeout           time.Duration
	OnChange          func(prev, next form.Snapshot)
	OnRefused         func(seq uint64, err error)
	OnIncompatible    func(schema int)
}

type Replica struct {
	client string
	t      Transport
	o      Options

	cur    atomic.Pointer[state]
	events chan func(*state) *state
	done   chan struct{}
	ready  chan struct{}
	stop   sync.Once

	debounce, maxWait *time.Timer
	idle              []chan struct{}
	retry             time.Duration
}

type pendingEdit struct {
	seq   uint64
	patch form.Patch
	sent  bool
}

type state struct {
	incompatible bool
	confirmed    form.Snapshot
	version      uint64
	incarnation  uint64
	pending      []pendingEdit
	draft        form.Patch
	nextSeq      uint64
	inFlight     bool
	loaded       bool
	shown        form.Snapshot
}

func New(client string, t Transport, o Options) *Replica {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	r := &Replica{
		client: client,
		t:      t,
		o:      o,
		events: make(chan func(*state) *state, 1024),
		done:   make(chan struct{}),
		ready:  make(chan struct{}),
	}
	r.cur.Store(&state{nextSeq: 1})
	go r.run()
	r.Reload()
	return r
}

func (r *Replica) Shown() form.Snapshot { return r.cur.Load().shown }

func (r *Replica) Version() uint64 { return r.cur.Load().version }

func (r *Replica) Pending() int {
	s := r.cur.Load()
	n := len(s.pending)
	if !s.draft.IsIdentity() {
		n++
	}
	return n
}

func (r *Replica) Ready() <-chan struct{} { return r.ready }

func (r *Replica) Edit(p form.Patch) {
	if p.IsIdentity() {
		return
	}
	r.post(func(s *state) *state { return r.edit(s, p) })
}

func (r *Replica) Delta(d rpc.FormDelta) {
	r.post(func(s *state) *state { return r.delta(s, d) })
}

func (r *Replica) Lost() { r.Reload() }

func (r *Replica) Reload() { r.post(func(s *state) *state { r.reload(); return s }) }

func (r *Replica) Flush(ctx context.Context) error {
	wait := make(chan struct{})
	r.post(func(s *state) *state {
		next := r.flushDraft(s)
		if len(next.pending) == 0 {
			close(wait)
		} else {
			r.idle = append(r.idle, wait)
		}
		return next
	})
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return errors.New("replica closed")
	}
}

func (r *Replica) Close() {
	r.stop.Do(func() { close(r.done) })
}

func (r *Replica) post(fn func(*state) *state) {
	select {
	case r.events <- fn:
	case <-r.done:
	}
}

func (r *Replica) run() {
	for {
		select {
		case <-r.done:
			r.stopTimers()
			return
		case fn := <-r.events:
			prev := r.cur.Load()
			next := fn(prev)
			if next == prev {
				continue
			}
			r.cur.Store(next)
			if next.loaded && !prev.loaded {
				close(r.ready)
			}
			if r.o.OnChange != nil && next.shown != prev.shown {
				r.o.OnChange(prev.shown, next.shown)
			}
			if len(next.pending) == 0 && next.draft.IsIdentity() {
				for _, w := range r.idle {
					close(w)
				}
				r.idle = nil
			}
		}
	}
}

func (r *Replica) edit(s *state, p form.Patch) *state {
	next := *s
	if merged, ok := form.TryMerge(s.draft, p); ok {
		next.draft = merged
	} else {
		next = *r.flushDraft(&next)
		next.draft = p
	}
	next.shown = s.shown.Apply(p)
	r.armTimers()
	return &next
}

func (r *Replica) flushDraft(s *state) *state {
	if s.draft.IsIdentity() {
		return s
	}
	next := *s
	next.pending = append(append([]pendingEdit(nil), s.pending...), pendingEdit{seq: s.nextSeq, patch: s.draft})
	next.nextSeq++
	next.draft = form.Patch{}
	return r.sendNext(&next)
}

func (r *Replica) sendNext(s *state) *state {
	if s.inFlight || !s.loaded || s.incompatible {
		return s
	}
	for i, pe := range s.pending {
		if pe.sent {
			continue
		}
		next := *s
		next.pending = append([]pendingEdit(nil), s.pending...)
		next.pending[i].sent = true
		next.inFlight = true
		req := rpc.SetRequest{Patch: pe.patch, Client: r.client, Seq: pe.seq}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), r.o.Timeout)
			defer cancel()
			resp, err := r.t.Set(ctx, req)
			r.post(func(s *state) *state { return r.answer(s, req.Seq, resp, err) })
		}()
		return &next
	}
	return s
}

func (r *Replica) answer(s *state, seq uint64, resp *rpc.SetResponse, err error) *state {
	next := *s
	next.inFlight = false
	var refusal *jkrpc.Error
	switch {
	case err == nil && resp.Outcome == rpc.OutcomeApplied && resp.Version > s.version:
		r.expect(resp.Version)
		return r.sendNext(&next)
	case err == nil:
		next.pending = without(s.pending, seq)
	case errors.As(err, &refusal) && refusal.Code != rpc.ErrCodeFormUnavailable:
		next.pending = without(s.pending, seq)
		if r.o.OnRefused != nil {
			r.o.OnRefused(seq, err)
		}
	default:
		next.pending = unsent(s.pending)
		r.reloadLater()
		return &next
	}
	recompute(&next)
	return r.sendNext(&next)
}

func (r *Replica) delta(s *state, d rpc.FormDelta) *state {
	if s.incompatible {
		return s
	}
	if d.Schema != rpc.FormDeltaSchema {
		next := *s
		next.incompatible = true
		if r.o.OnIncompatible != nil {
			r.o.OnIncompatible(d.Schema)
		}
		return &next
	}
	if !s.loaded {
		return s
	}
	if d.Incarnation != s.incarnation {
		if d.Incarnation > s.incarnation {
			r.reload()
		}
		return s
	}
	if d.Version <= s.version {
		return s
	}
	if d.Version != s.version+1 {
		r.reload()
		return s
	}
	next := *s
	next.confirmed = s.confirmed.Apply(d.Patch)
	next.version = d.Version
	if d.Client == r.client && d.Seq > 0 {
		next.pending = settled(s.pending, d.Seq)
	}
	recompute(&next)
	return r.sendNext(&next)
}

func (r *Replica) reload() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), r.o.Timeout)
		defer cancel()
		resp, err := r.t.Load(ctx, r.client)
		if err != nil {
			r.post(func(s *state) *state { r.reloadLater(); return s })
			return
		}
		r.post(func(s *state) *state { return r.loaded(s, resp) })
	}()
}

func (r *Replica) expect(version uint64) {
	time.AfterFunc(r.o.Timeout, func() {
		r.post(func(s *state) *state {
			if s.version < version {
				r.reload()
			}
			return s
		})
	})
}

func (r *Replica) reloadLater() {
	switch {
	case r.retry == 0:
		r.retry = 50 * time.Millisecond
	case r.retry < 5*time.Second:
		r.retry *= 2
	}
	d := r.retry
	time.AfterFunc(d, func() { r.Reload() })
}

func (r *Replica) loaded(s *state, resp *rpc.FormResponse) *state {
	r.retry = 0
	if s.loaded && (resp.Incarnation < s.incarnation || resp.Incarnation == s.incarnation && resp.Version < s.version) {
		return s
	}
	next := *s
	next.confirmed, next.version, next.incarnation = resp.Snapshot, resp.Version, resp.Incarnation
	next.loaded = true
	next.pending = settled(s.pending, resp.LastSeq)
	if !s.inFlight {
		next.pending = unsent(next.pending)
	}
	recompute(&next)
	return r.sendNext(&next)
}

func recompute(s *state) {
	shown := s.confirmed
	for _, pe := range s.pending {
		shown = shown.Apply(pe.patch)
	}
	s.shown = shown.Apply(s.draft)
}

func (r *Replica) armTimers() {
	if r.o.Debounce <= 0 {
		r.post(func(s *state) *state { return r.flushDraft(s) })
		return
	}
	flush := func() { r.post(func(s *state) *state { r.stopTimers(); return r.flushDraft(s) }) }
	if r.debounce == nil {
		r.debounce = time.AfterFunc(r.o.Debounce, flush)
	} else {
		r.debounce.Reset(r.o.Debounce)
	}
	if r.maxWait == nil && r.o.MaxWait > 0 {
		r.maxWait = time.AfterFunc(r.o.MaxWait, flush)
	}
}

func (r *Replica) stopTimers() {
	if r.debounce != nil {
		r.debounce.Stop()
		r.debounce = nil
	}
	if r.maxWait != nil {
		r.maxWait.Stop()
		r.maxWait = nil
	}
}

func without(ps []pendingEdit, seq uint64) []pendingEdit {
	out := make([]pendingEdit, 0, len(ps))
	for _, pe := range ps {
		if pe.seq != seq {
			out = append(out, pe)
		}
	}
	return out
}

func settled(ps []pendingEdit, through uint64) []pendingEdit {
	out := make([]pendingEdit, 0, len(ps))
	for _, pe := range ps {
		if pe.seq > through {
			out = append(out, pe)
		}
	}
	return out
}

func unsent(ps []pendingEdit) []pendingEdit {
	out := make([]pendingEdit, len(ps))
	for i, pe := range ps {
		pe.sent = false
		out[i] = pe
	}
	return out
}
