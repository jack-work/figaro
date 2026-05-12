package store

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// LogCache holds shared Log instances behind refcounted, TTL-evicted
// handles. Multiple callers acquiring the same aria's log get the
// same underlying instance — so the angelus (serving read RPCs) and
// the per-aria agent share one figwal cache, lock-free reads and
// all. Eviction only fires after every borrower has released and
// the entry has sat idle for the TTL.
//
// Lifecycle model:
//
//	angelus boot:     cache = NewLogCache(backend, ttl)
//	aria.read RPC:    log, rel, _ := cache.AcquireIR(id);  defer rel(); read
//	agent spawn:      log, rel, _ := cache.AcquireIR(id);  agent holds rel
//	agent kill:       rel(); entry may evict after ttl
//	idle aria:        no Acquires for ttl → gc closes the inner Log
//
// AcquireIR/AcquireTranslation open via the wrapped Backend on miss.
type LogCache struct {
	inner Backend
	ttl   time.Duration

	mu     sync.Mutex
	irs    map[string]*irRef
	trs    map[trKey]*trRef
	closed bool

	gcStop chan struct{}
	gcDone chan struct{}
}

type trKey struct{ aria, provider string }

type irRef struct {
	log        Log[message.Message]
	refs       int
	lastAccess time.Time
}

type trRef struct {
	log        Log[[]json.RawMessage]
	refs       int
	lastAccess time.Time
}

// NewLogCache wraps inner with a refcount+TTL cache. The GC loop
// runs at ttl/4 cadence (capped at 5s) and evicts entries with
// refs==0 and lastAccess older than ttl.
func NewLogCache(inner Backend, ttl time.Duration) *LogCache {
	c := &LogCache{
		inner:  inner,
		ttl:    ttl,
		irs:    make(map[string]*irRef),
		trs:    make(map[trKey]*trRef),
		gcStop: make(chan struct{}),
		gcDone: make(chan struct{}),
	}
	go c.gcLoop()
	return c
}

// AcquireIR returns the IR log for ariaID, opening it on miss.
// Callers must invoke the returned release func when done.
func (c *LogCache) AcquireIR(ariaID string) (Log[message.Message], func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, errors.New("log cache: closed")
	}
	e, ok := c.irs[ariaID]
	if !ok {
		log, err := c.inner.Open(ariaID)
		if err != nil {
			return nil, nil, err
		}
		e = &irRef{log: log}
		c.irs[ariaID] = e
	}
	e.refs++
	e.lastAccess = time.Now()
	released := false
	release := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if released {
			return
		}
		released = true
		if cur, ok := c.irs[ariaID]; ok {
			cur.refs--
			cur.lastAccess = time.Now()
		}
	}
	return e.log, release, nil
}

// AcquireTranslation returns the translator cache log for the given
// (aria, provider) pair.
func (c *LogCache) AcquireTranslation(ariaID, providerName string) (Log[[]json.RawMessage], func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, errors.New("log cache: closed")
	}
	k := trKey{ariaID, providerName}
	e, ok := c.trs[k]
	if !ok {
		log, err := c.inner.OpenTranslation(ariaID, providerName)
		if err != nil {
			return nil, nil, err
		}
		e = &trRef{log: log}
		c.trs[k] = e
	}
	e.refs++
	e.lastAccess = time.Now()
	released := false
	release := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if released {
			return
		}
		released = true
		if cur, ok := c.trs[k]; ok {
			cur.refs--
			cur.lastAccess = time.Now()
		}
	}
	return e.log, release, nil
}

// Backend returns the wrapped Backend for callers that need the
// non-log methods (Meta/SetMeta/List/Remove).
func (c *LogCache) Backend() Backend { return c.inner }

// Close stops the GC loop and closes every cached log, regardless
// of refcount. Outstanding release funcs become no-ops.
func (c *LogCache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	close(c.gcStop)
	<-c.gcDone

	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for id, e := range c.irs {
		if err := e.log.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(c.irs, id)
	}
	for k, e := range c.trs {
		if err := e.log.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(c.trs, k)
	}
	return errors.Join(errs...)
}

func (c *LogCache) gcLoop() {
	defer close(c.gcDone)
	tick := c.ttl / 4
	if tick > 5*time.Second {
		tick = 5 * time.Second
	}
	if tick < 100*time.Millisecond {
		tick = 100 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-c.gcStop:
			return
		case <-t.C:
			c.gc()
		}
	}
}

func (c *LogCache) gc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	now := time.Now()
	for id, e := range c.irs {
		if e.refs == 0 && now.Sub(e.lastAccess) > c.ttl {
			_ = e.log.Close()
			delete(c.irs, id)
		}
	}
	for k, e := range c.trs {
		if e.refs == 0 && now.Sub(e.lastAccess) > c.ttl {
			_ = e.log.Close()
			delete(c.trs, k)
		}
	}
}
