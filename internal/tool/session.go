package tool

import (
	"fmt"
	"sort"
	"sync"
	"syscall"
	"time"
)

// SessionState is the lifecycle state of a backgrounded exec session.
type SessionState string

const (
	SessionRunning  SessionState = "running"
	SessionExited   SessionState = "exited"
	SessionKilled   SessionState = "killed"
	SessionTimedOut SessionState = "timed_out"
)

const (
	// DefaultSessionTTL is how long a finished session is retained
	// before the registry reaps it.
	DefaultSessionTTL = 5 * time.Minute
	// sessionBufferCap bounds a session's retained aggregate output;
	// older bytes are dropped from the front once exceeded.
	sessionBufferCap = 256 * 1024
)

// capBuffer accumulates bytes, dropping from the front once it exceeds
// cap. Zero cap means unbounded.
type capBuffer struct {
	buf []byte
	cap int
}

func (c *capBuffer) Write(p []byte) {
	c.buf = append(c.buf, p...)
	if c.cap > 0 && len(c.buf) > c.cap {
		c.buf = append([]byte(nil), c.buf[len(c.buf)-c.cap:]...)
	}
}

func (c *capBuffer) String() string { return string(c.buf) }

// ExecSession is a backgrounded command whose lifetime outlives the
// tool call that launched it. Output streams into two buffers: agg
// holds the whole (capped) transcript for `log`; unread holds bytes not
// yet drained by `poll`.
type ExecSession struct {
	ID        string
	Scope     string
	Command   string
	StartedAt time.Time

	mu       sync.Mutex
	pid      int
	state    SessionState
	exitCode int
	endedAt  time.Time
	agg      capBuffer
	unread   capBuffer
	killing  bool
	timedOut bool
	proc     Process

	done chan struct{}
}

// writeChunk fans a streamed output chunk into both buffers.
func (s *ExecSession) writeChunk(p []byte) {
	s.mu.Lock()
	s.agg.Write(p)
	s.unread.Write(p)
	s.mu.Unlock()
}

// supervise attaches a started process and watches it to completion,
// enforcing the hard-kill deadline. It is deliberately free of any
// caller context: only the timeout (or an explicit Kill) ends the
// process early — a tool-call abort never does.
func (s *ExecSession) supervise(proc Process, hardTimeout time.Duration) {
	s.mu.Lock()
	s.proc = proc
	s.pid = proc.Pid()
	s.mu.Unlock()

	exited := make(chan int, 1)
	go func() { exited <- proc.Wait() }()

	go func() {
		var timeoutCh <-chan time.Time
		if hardTimeout > 0 {
			timer := time.NewTimer(hardTimeout)
			defer timer.Stop()
			timeoutCh = timer.C
		}
		select {
		case code := <-exited:
			s.finish(code)
		case <-timeoutCh:
			s.mu.Lock()
			s.timedOut = true
			s.mu.Unlock()
			proc.Signal(syscall.SIGKILL)
			s.finish(<-exited)
		}
		close(s.done)
	}()
}

func (s *ExecSession) finish(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exitCode = code
	s.endedAt = time.Now()
	switch {
	case s.timedOut:
		s.state = SessionTimedOut
	case s.killing:
		s.state = SessionKilled
	default:
		s.state = SessionExited
	}
}

// Done closes when the process has fully terminated for any reason.
func (s *ExecSession) Done() <-chan struct{} { return s.done }

// Kill SIGKILLs the process group. No-op if already finished.
func (s *ExecSession) Kill() error {
	s.mu.Lock()
	if s.state != SessionRunning || s.proc == nil {
		s.mu.Unlock()
		return nil
	}
	s.killing = true
	proc := s.proc
	s.mu.Unlock()
	return proc.Signal(syscall.SIGKILL)
}

// WriteStdin feeds bytes to the running process's stdin.
func (s *ExecSession) WriteStdin(p []byte) error {
	s.mu.Lock()
	proc, running := s.proc, s.state == SessionRunning
	s.mu.Unlock()
	if proc == nil || !running {
		return fmt.Errorf("session %s is not running", s.ID)
	}
	_, err := proc.Write(p)
	return err
}

// Poll returns and clears the output buffered since the last poll.
func (s *ExecSession) Poll() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.unread.String()
	s.unread = capBuffer{cap: sessionBufferCap}
	return out
}

// Log returns the full (capped) output transcript without draining.
func (s *ExecSession) Log() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agg.String()
}

// SessionInfo is an immutable snapshot for listing/status.
type SessionInfo struct {
	ID        string
	Scope     string
	Command   string
	Pid       int
	State     SessionState
	ExitCode  int
	StartedAt time.Time
	EndedAt   time.Time
}

func (s *ExecSession) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		ID:        s.ID,
		Scope:     s.Scope,
		Command:   s.Command,
		Pid:       s.pid,
		State:     s.state,
		ExitCode:  s.exitCode,
		StartedAt: s.StartedAt,
		EndedAt:   s.endedAt,
	}
}

func (s *ExecSession) finishedAt() (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == SessionRunning {
		return false, time.Time{}
	}
	return true, s.endedAt
}

// SessionRegistry is a concurrency-safe, scope-keyed set of exec
// sessions. Finished sessions older than ttl are reaped lazily on
// access.
type SessionRegistry struct {
	mu       sync.Mutex
	seq      uint64
	ttl      time.Duration
	sessions map[string]*ExecSession
}

// NewSessionRegistry returns a registry that reaps finished sessions
// ttl after they end. ttl <= 0 disables reaping.
func NewSessionRegistry(ttl time.Duration) *SessionRegistry {
	return &SessionRegistry{ttl: ttl, sessions: make(map[string]*ExecSession)}
}

// Create registers a fresh running session under scope and returns it.
// The caller is expected to start a process and hand it to supervise.
func (r *SessionRegistry) Create(scope, command string) *ExecSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked()
	r.seq++
	s := &ExecSession{
		ID:        fmt.Sprintf("bg-%d", r.seq),
		Scope:     scope,
		Command:   command,
		StartedAt: time.Now(),
		state:     SessionRunning,
		agg:       capBuffer{cap: sessionBufferCap},
		unread:    capBuffer{cap: sessionBufferCap},
		done:      make(chan struct{}),
	}
	r.sessions[s.ID] = s
	return s
}

// Get returns the session with id, scoped to scope.
func (r *SessionRegistry) Get(scope, id string) (*ExecSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked()
	s, ok := r.sessions[id]
	if !ok || s.Scope != scope {
		return nil, false
	}
	return s, true
}

// List returns the sessions under scope, oldest first.
func (r *SessionRegistry) List(scope string) []*ExecSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked()
	out := make([]*ExecSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		if s.Scope == scope {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// Remove drops a session from the registry, returning it if present.
// It does not kill the process; callers wanting that should Kill first.
func (r *SessionRegistry) Remove(scope, id string) (*ExecSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.Scope != scope {
		return nil, false
	}
	delete(r.sessions, id)
	return s, true
}

func (r *SessionRegistry) reapLocked() {
	if r.ttl <= 0 {
		return
	}
	now := time.Now()
	for id, s := range r.sessions {
		if done, ended := s.finishedAt(); done && now.Sub(ended) > r.ttl {
			delete(r.sessions, id)
		}
	}
}
