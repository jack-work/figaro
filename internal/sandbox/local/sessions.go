package local

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
)

const (
	DefaultTTL = 5 * time.Minute
	bufferCap  = 256 * 1024
)

type Host struct {
	mu       sync.Mutex
	seq      uint64
	ttl      time.Duration
	sessions map[string]*session
}

func NewHost(ttl time.Duration) *Host {
	return &Host{ttl: ttl, sessions: map[string]*session{}}
}

func (h *Host) Scoped(scope string, start func(sandbox.ExecRequest) (sandbox.Process, error)) sandbox.Sessions {
	return &scoped{host: h, scope: scope, start: start}
}

func (h *Host) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reap()
	return len(h.sessions)
}

func (h *Host) KillScope(scope string) int {
	h.mu.Lock()
	var victims []*session
	for id, s := range h.sessions {
		if s.scope == scope {
			victims = append(victims, s)
			delete(h.sessions, id)
		}
	}
	h.mu.Unlock()
	for _, s := range victims {
		s.Kill()
	}
	return len(victims)
}

func (h *Host) reap() {
	if h.ttl <= 0 {
		return
	}
	for id, s := range h.sessions {
		if ended := s.ended(); !ended.IsZero() && time.Since(ended) > h.ttl {
			delete(h.sessions, id)
		}
	}
}

type scoped struct {
	host  *Host
	scope string
	start func(sandbox.ExecRequest) (sandbox.Process, error)
}

func (v *scoped) Start(req sandbox.ExecRequest) (sandbox.Session, error) {
	s := &session{scope: v.scope, command: req.Command, startedAt: time.Now()}
	s.agg.cap, s.unread.cap = bufferCap, bufferCap
	onChunk := req.OnChunk
	req.OnChunk = func(b []byte) {
		s.mu.Lock()
		s.agg.write(b)
		s.unread.write(b)
		s.mu.Unlock()
		if onChunk != nil {
			onChunk(b)
		}
	}
	proc, err := v.start(req)
	if err != nil {
		return nil, err
	}
	s.Process = proc
	go func() {
		<-proc.Done()
		s.mu.Lock()
		s.endedAt = time.Now()
		s.mu.Unlock()
	}()
	v.host.mu.Lock()
	v.host.reap()
	v.host.seq++
	s.id = fmt.Sprintf("bg-%d", v.host.seq)
	v.host.sessions[s.id] = s
	v.host.mu.Unlock()
	return s, nil
}

func (v *scoped) Get(id string) (sandbox.Session, error) {
	v.host.mu.Lock()
	defer v.host.mu.Unlock()
	v.host.reap()
	s, ok := v.host.sessions[id]
	if !ok || s.scope != v.scope {
		return nil, sandbox.ErrNoSession
	}
	return s, nil
}

func (v *scoped) List() []sandbox.SessionInfo {
	v.host.mu.Lock()
	defer v.host.mu.Unlock()
	v.host.reap()
	var out []sandbox.SessionInfo
	for _, s := range v.host.sessions {
		if s.scope == v.scope {
			out = append(out, s.Info())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func (v *scoped) Remove(id string) error {
	v.host.mu.Lock()
	s, ok := v.host.sessions[id]
	if ok && s.scope == v.scope {
		delete(v.host.sessions, id)
	}
	v.host.mu.Unlock()
	if !ok || s.scope != v.scope {
		return sandbox.ErrNoSession
	}
	return s.Kill()
}

type session struct {
	sandbox.Process
	id, scope, command string
	startedAt          time.Time

	mu          sync.Mutex
	agg, unread capBuffer
	endedAt     time.Time
	killed      bool
}

func (s *session) ID() string { return s.id }

func (s *session) Kill() error {
	s.mu.Lock()
	s.killed = true
	s.mu.Unlock()
	select {
	case <-s.Done():
		return nil
	default:
		return s.Process.Kill()
	}
}

func (s *session) Poll() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.unread.buf
	s.unread = capBuffer{cap: bufferCap}
	return out
}

func (s *session) Log() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agg.buf
}

func (s *session) ended() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endedAt
}

func (s *session) Info() sandbox.SessionInfo {
	info := sandbox.SessionInfo{ID: s.id, Command: s.command, State: sandbox.SessionRunning, StartedAt: s.startedAt}
	select {
	case <-s.Done():
	default:
		s.mu.Lock()
		if s.killed {
			info.State = sandbox.SessionKilling
		}
		s.mu.Unlock()
		return info
	}
	res := s.Wait()
	s.mu.Lock()
	info.EndedAt, info.ExitCode = s.endedAt, res.ExitCode
	switch {
	case res.TimedOut:
		info.State = sandbox.SessionTimedOut
	case s.killed || res.Canceled:
		info.State = sandbox.SessionKilled
	default:
		info.State = sandbox.SessionExited
	}
	s.mu.Unlock()
	return info
}

type capBuffer struct {
	buf []byte
	cap int
}

func (c *capBuffer) write(p []byte) {
	c.buf = append(c.buf, p...)
	if c.cap > 0 && len(c.buf) > c.cap {
		c.buf = append([]byte(nil), c.buf[len(c.buf)-c.cap:]...)
	}
}
