package store

import (
	"log/slog"
	"sync"
	"time"
)

var (
	syncWaitBudget = 250 * time.Millisecond
	syncSlowWarn   = time.Second
)

type syncKey struct{ aria, channel string }

type channelSyncer struct {
	key  syncKey
	sync func(uint64) error

	mu      sync.Mutex
	want    uint64
	done    uint64
	running bool
	waiters []syncWaiter
	idle    chan struct{}
}

type syncWaiter struct {
	idx uint64
	ch  chan error
}

type syncers struct {
	mu sync.Mutex
	by map[syncKey]*channelSyncer

	before func()
}

func (s *syncers) get(key syncKey, sync func(uint64) error) *channelSyncer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[syncKey]*channelSyncer{}
	}
	cs, ok := s.by[key]
	if !ok {
		cs = &channelSyncer{key: key, sync: sync}
		s.by[key] = cs
	}
	return cs
}

func (s *syncers) all() []*channelSyncer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*channelSyncer, 0, len(s.by))
	for _, cs := range s.by {
		out = append(out, cs)
	}
	return out
}

func (s *syncers) forget(aria string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.by {
		if key.aria == aria {
			delete(s.by, key)
		}
	}
}

func (s *syncers) flush() {
	for _, cs := range s.all() {
		cs.flush()
	}
}

func (cs *channelSyncer) through(idx uint64, budget time.Duration) error {
	ch := make(chan error, 1)
	cs.mu.Lock()
	if idx <= cs.done {
		cs.mu.Unlock()
		return nil
	}
	if idx > cs.want {
		cs.want = idx
	}
	cs.waiters = append(cs.waiters, syncWaiter{idx: idx, ch: ch})
	if !cs.running {
		cs.running = true
		cs.idle = make(chan struct{})
		go cs.run()
	}
	cs.mu.Unlock()
	t := time.NewTimer(budget)
	defer t.Stop()
	select {
	case err := <-ch:
		return err
	case <-t.C:
		return nil
	}
}

func (cs *channelSyncer) run() {
	for {
		cs.mu.Lock()
		if cs.want <= cs.done {
			cs.running = false
			close(cs.idle)
			cs.mu.Unlock()
			return
		}
		target := cs.want
		cs.mu.Unlock()

		started := time.Now()
		err := cs.sync(target)
		if took := time.Since(started); took >= syncSlowWarn {
			slog.Warn("wal sync slow", "aria", cs.key.aria, "channel", cs.key.channel, "ms", took.Milliseconds())
		}
		if err != nil {
			slog.Error("wal sync failed; records past the last durable index are lost if the machine stops now",
				"aria", cs.key.aria, "channel", cs.key.channel, "through", target, "err", err)
		}

		cs.mu.Lock()
		if err == nil && target > cs.done {
			cs.done = target
		}
		if err != nil {
			cs.want = cs.done
		}
		kept := cs.waiters[:0]
		for _, w := range cs.waiters {
			switch {
			case err != nil:
				w.ch <- err
			case w.idx <= cs.done:
				w.ch <- nil
			default:
				kept = append(kept, w)
			}
		}
		cs.waiters = kept
		cs.mu.Unlock()
	}
}

func (cs *channelSyncer) flush() {
	cs.mu.Lock()
	if !cs.running {
		cs.mu.Unlock()
		return
	}
	idle := cs.idle
	cs.mu.Unlock()
	<-idle
}

func (s *XwalStore) syncChannel(aria, channel string, idx uint64) error {
	if err := s.trunks.WriteChannelThrough(aria, channel, idx); err != nil {
		return err
	}
	cs := s.syncs.get(syncKey{aria: aria, channel: channel}, func(through uint64) error {
		if s.syncs.before != nil {
			s.syncs.before()
		}
		if _, ok := s.trunks.Kind(aria); !ok {
			return nil
		}
		return s.trunks.SyncChannelThrough(aria, channel, through)
	})
	return cs.through(idx, syncWaitBudget)
}

// FlushSyncs waits until every deferred channel sync has reached disk.
func (s *XwalStore) FlushSyncs() { s.syncs.flush() }
