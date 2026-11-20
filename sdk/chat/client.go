package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

type Client struct {
	ep          transport.Endpoint
	dial        func(ctx context.Context, id string, notify sdk.NotifyHandler) (ariaConn, error)
	dialAngelus func() (angelusConn, error)

	ReconnectFor time.Duration
	CheckEvery   time.Duration
	IdleClose    time.Duration

	done    chan struct{}
	mu      sync.Mutex
	angelus angelusConn
	threads map[string]*thread
	closed  bool
}

func New(ep transport.Endpoint) *Client {
	c := &Client{ep: ep, ReconnectFor: 30 * time.Second, CheckEvery: 15 * time.Second, IdleClose: 5 * time.Minute, done: make(chan struct{}), threads: map[string]*thread{}}
	c.dial = c.dialAria
	c.dialAngelus = func() (angelusConn, error) { return sdk.DialAngelus(ep) }
	return c
}

var ErrClosed = errors.New("chat: client closed")

func (c *Client) Close() error {
	c.mu.Lock()
	if !c.closed {
		close(c.done)
	}
	c.closed = true
	threads := c.threads
	c.threads = map[string]*thread{}
	a := c.angelus
	c.angelus = nil
	c.mu.Unlock()
	for _, t := range threads {
		t.shut(ErrClosed.Error())
	}
	if a != nil {
		return a.Close()
	}
	return nil
}

type angelusConn interface {
	Fork(ctx context.Context, figaroID string, atTurn, atLT uint64, outfits []string, patch *rpc.FormPatch) (*rpc.ForkResponse, error)
	Attach(ctx context.Context, figaroID string) (*rpc.AttachResponse, error)
	Close() error
}

func (c *Client) withAngelus(ctx context.Context, retry bool, fn func(angelusConn) error) error {
	a, err := c.tryAngelus(fn)
	if err == nil || ctx.Err() != nil || errors.Is(err, ErrClosed) || refused(err) {
		return err
	}
	c.mu.Lock()
	if a != nil && c.angelus == a {
		_ = a.Close()
		c.angelus = nil
	}
	c.mu.Unlock()
	if !retry {
		return err
	}
	_, err = c.tryAngelus(fn)
	return err
}

func refused(err error) bool {
	var refusal *jkrpc.Error
	return errors.As(err, &refusal)
}

func (c *Client) tryAngelus(fn func(angelusConn) error) (angelusConn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if c.angelus == nil {
		a, err := c.dialAngelus()
		if err != nil {
			c.mu.Unlock()
			return nil, fmt.Errorf("dial figaro: %w", err)
		}
		c.angelus = a
	}
	a := c.angelus
	c.mu.Unlock()
	return a, fn(a)
}

func (c *Client) Open(ctx context.Context, root string) (string, error) {
	var id string
	err := c.withAngelus(ctx, false, func(a angelusConn) error {
		resp, err := a.Fork(ctx, root, 0, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("fork %s: %w", root, err)
		}
		id = resp.Alternative
		return nil
	})
	return id, err
}

func (c *Client) Prompt(ctx context.Context, threadID, text string) (<-chan Event, error) {
	t, conn, err := c.thread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	cursor, active, err := conn.Qua(ctx, text, nil)
	if err != nil {
		return nil, fmt.Errorf("send to %s: %w", threadID, err)
	}
	return t.subscribe(ctx, &sub{after: uint64(cursor), steer: active}, nil), nil
}

func (c *Client) Resume(ctx context.Context, threadID string, from Cursor) (<-chan Event, error) {
	if from.Turn == 0 {
		return nil, errors.New("chat: resume needs a cursor with a turn")
	}
	t, _, err := c.thread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return t.subscribe(ctx, &sub{after: from.Turn - 1, r: newReader(from), resumed: true}, []uint64{from.Turn}), nil
}

func (c *Client) Cancel(ctx context.Context, threadID string) error {
	_, conn, err := c.thread(ctx, threadID)
	if err != nil {
		return err
	}
	return conn.Interrupt(ctx)
}

func (c *Client) thread(ctx context.Context, id string) (*thread, ariaConn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClosed
	}
	t := c.threads[id]
	if t == nil {
		t = newThread(c, id)
		c.threads[id] = t
	}
	c.mu.Unlock()
	t.touch()
	conn, err := t.connect(ctx)
	return t, conn, err
}

func newThread(c *Client, id string) *thread {
	return &thread{c: c, id: id, folds: map[uint64]*turnFold{}, want: map[uint64]bool{}, subs: map[*sub]struct{}{}}
}

type ariaConn interface {
	Qua(ctx context.Context, text string, form *rpc.FormInput) (int, bool, error)
	Read(ctx context.Context, at aria.Anchor, budget int) (aria.Page, error)
	ReadBefore(ctx context.Context, at aria.Anchor, budget int) (aria.Page, error)
	FormOf(ctx context.Context, intrinsic string) (*rpc.FormResponse, error)
	Interrupt(ctx context.Context) error
	Close() error
	Done() <-chan struct{}
}

func (c *Client) dialAria(ctx context.Context, id string, notify sdk.NotifyHandler) (ariaConn, error) {
	var canonical string
	if err := c.withAngelus(ctx, true, func(a angelusConn) error {
		att, err := a.Attach(ctx, id)
		if err != nil {
			return fmt.Errorf("attach %s: %w", id, err)
		}
		canonical = att.FigaroID
		return nil
	}); err != nil {
		return nil, err
	}
	conn, err := sdk.DialAria(sdk.Addr{Door: c.ep, ID: canonical}, notify)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", id, err)
	}
	return conn, nil
}

type note struct {
	part *aria.TurnPart
	rt   *rpc.FormDelta
}

type thread struct {
	c  *Client
	id string

	dial sync.Mutex

	mu           sync.Mutex
	conn         ariaConn
	gen          uint64
	reconnecting bool
	closed       bool
	lastUsed     time.Time
	idleTimer    *time.Timer
	watching     bool
	subSeq       uint64
	folds        map[uint64]*turnFold
	top          uint64
	syncing      bool
	want         map[uint64]bool
	rt           runtimeView
	wantRT       bool
	rtReady      chan struct{}
	buf          []note
	subs         map[*sub]struct{}
}

func alive(conn ariaConn) bool {
	if conn == nil {
		return false
	}
	select {
	case <-conn.Done():
		return false
	default:
		return true
	}
}

func (t *thread) connect(ctx context.Context) (ariaConn, error) {
	t.dial.Lock()
	defer t.dial.Unlock()
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if alive(conn) {
		return conn, nil
	}
	conn, err := t.c.dial(ctx, t.id, t.notify)
	if err != nil {
		return nil, err
	}
	ready, err := t.attach(conn)
	if err != nil {
		return nil, err
	}
	go t.watch(conn)
	select {
	case <-ready:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-conn.Done():
		return nil, errors.New("the connection to figaro closed while reading its state")
	}
}

func (t *thread) attach(conn ariaConn) (chan struct{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		_ = conn.Close()
		return nil, ErrClosed
	}
	t.conn = conn
	t.gen++
	t.folds = map[uint64]*turnFold{}
	t.buf = nil
	t.want = map[uint64]bool{}
	for s := range t.subs {
		t.want[s.next()] = true
	}
	t.wantRT = true
	ready := make(chan struct{})
	t.rtReady = ready
	t.startSync()
	return ready, nil
}

func (t *thread) watch(conn ariaConn) {
	<-conn.Done()
	t.mu.Lock()
	if t.conn == conn {
		t.conn = nil
	}
	t.redialLocked()
	t.mu.Unlock()
}

func (t *thread) redialLocked() {
	if t.conn != nil || t.reconnecting || len(t.subs) == 0 {
		return
	}
	t.reconnecting = true
	go t.reconnect()
}

func (t *thread) reconnect() {
	deadline := time.Now().Add(t.c.ReconnectFor)
	wait := 100 * time.Millisecond
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := t.connect(ctx)
		cancel()
		if err == nil {
			t.mu.Lock()
			t.reconnecting = false
			t.mu.Unlock()
			return
		}
		if errors.Is(err, ErrClosed) || time.Now().Add(wait).After(deadline) {
			t.mu.Lock()
			t.reconnecting = false
			t.mu.Unlock()
			t.fail("lost the connection to figaro: " + err.Error())
			return
		}
		time.Sleep(wait)
		wait = min(2*wait, 2*time.Second)
	}
}

func (t *thread) shut(why string) {
	t.mu.Lock()
	t.closed = true
	conn := t.conn
	t.conn = nil
	t.mu.Unlock()
	t.fail(why)
	if conn != nil {
		_ = conn.Close()
	}
}

func (t *thread) fail(why string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for s := range t.subs {
		cur := Cursor{Turn: s.next()}
		if s.r != nil {
			cur = s.r.cur
		}
		s.push(Event{Kind: Failed, Text: why, Cursor: cur}, true)
		t.leave(s)
	}
}

func (t *thread) notify(method string, params json.RawMessage) {
	var notes []note
	switch method {
	case rpc.MethodAriaFrame:
		var p aria.Page
		if json.Unmarshal(params, &p) != nil {
			return
		}
		for i := range p.Parts {
			notes = append(notes, note{part: &p.Parts[i]})
		}
	case rpc.MethodFormDelta:
		var d rpc.FormDelta
		if json.Unmarshal(params, &d) != nil || d.Intrinsic != runtimeIntrinsic {
			return
		}
		notes = append(notes, note{rt: &d})
	default:
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.syncing {
		t.buf = append(t.buf, notes...)
		return
	}
	for i, n := range notes {
		if !t.process(n) {
			t.buf = append(t.buf, notes[i+1:]...)
			return
		}
	}
}

func (t *thread) process(n note) bool {
	if n.rt != nil {
		switch t.rt.delta(*n.rt) {
		case gap:
			t.wantRT = true
			t.startSync()
			return false
		case fresh:
			t.settle()
		}
		return true
	}
	p := n.part
	f := t.fold(p.ID)
	switch f.apply(*p) {
	case gap:
		t.want[p.ID] = true
		t.startSync()
		return false
	case fresh:
		t.deliver(f)
	}
	return true
}

func (t *thread) fold(id uint64) *turnFold {
	f := t.folds[id]
	if f == nil {
		f = newTurnFold(id)
		t.folds[id] = f
	}
	if id > t.top {
		t.top = id
		t.trim()
	}
	return f
}

func (t *thread) trim() {
	floor := uint64(0)
	if t.top >= keptTurns {
		floor = t.top - keptTurns + 1
	}
	for s := range t.subs {
		floor = min(floor, s.next())
	}
	for id := range t.folds {
		if id < floor {
			delete(t.folds, id)
		}
	}
}

const keptTurns = 4

func (t *thread) startSync() {
	if t.syncing || len(t.want) == 0 && !t.wantRT {
		return
	}
	t.syncing = true
	go t.sync()
}

func (t *thread) sync() {
	var failing time.Time
	wait := 100 * time.Millisecond
	for {
		t.mu.Lock()
		if len(t.want) > 0 || t.wantRT {
			if !alive(t.conn) {
				t.syncing = false
				t.mu.Unlock()
				return
			}
			turns := make([]uint64, 0, len(t.want))
			for id := range t.want {
				turns = append(turns, id)
			}
			t.want, t.wantRT = map[uint64]bool{}, false
			conn, gen := t.conn, t.gen
			t.mu.Unlock()
			err := t.seed(conn, gen, turns)
			if err == nil {
				failing, wait = time.Time{}, 100*time.Millisecond
				continue
			}
			if failing.IsZero() {
				failing = time.Now()
			}
			if time.Since(failing) > t.c.ReconnectFor {
				t.failFrom(turns, "read the thread: "+err.Error())
				failing = time.Time{}
				continue
			}
			time.Sleep(wait)
			wait = min(2*wait, 2*time.Second)
			continue
		}
		if len(t.buf) == 0 {
			t.syncing = false
			t.mu.Unlock()
			return
		}
		n := t.buf[0]
		t.buf = t.buf[1:]
		t.process(n)
		t.mu.Unlock()
	}
}

func (t *thread) seed(conn ariaConn, gen uint64, turns []uint64) error {
	sort.Slice(turns, func(i, j int) bool { return turns[i] < turns[j] })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rt, err := conn.FormOf(ctx, runtimeIntrinsic)
	dormant := isDormant(err)
	if dormant {
		err = nil
	}
	var parts []aria.TurnPart
	if err == nil && len(turns) > 0 {
		parts, err = readTurns(ctx, conn, turns[0])
	}
	if err == nil && dormant {
		rt = asleep(parts)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen != t.gen {
		return nil
	}
	if err != nil {
		if alive(conn) {
			for _, id := range turns {
				t.want[id] = true
			}
			t.wantRT = true
			return err
		}
		return nil
	}
	t.adopt(rt, parts)
	return nil
}

func (t *thread) failFrom(turns []uint64, why string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	floor := ^uint64(0)
	for _, id := range turns {
		floor = min(floor, id)
	}
	for s := range t.subs {
		if len(turns) == 0 || s.next() >= floor {
			s.push(Event{Kind: Failed, Text: why, Cursor: Cursor{Turn: s.next()}}, true)
			t.leave(s)
		}
	}
	t.want, t.wantRT = map[uint64]bool{}, false
}

func (t *thread) adopt(rt *rpc.FormResponse, parts []aria.TurnPart) {
	if rt != nil {
		inc := t.rt.inc
		if t.rt.reset(rt) && t.rt.inc != inc {
			for s := range t.subs {
				s.rtVer = 0
			}
		}
		if t.rtReady != nil {
			close(t.rtReady)
			t.rtReady = nil
		}
	}
	rebuilt := map[uint64]*turnFold{}
	for _, p := range parts {
		f := rebuilt[p.ID]
		if f == nil {
			f = newTurnFold(p.ID)
			rebuilt[p.ID] = f
		}
		f.apply(p)
	}
	ids := make([]uint64, 0, len(rebuilt))
	for id, f := range rebuilt {
		if old := t.folds[id]; old != nil && old.ended {
			continue
		}
		t.folds[id] = f
		t.top = max(t.top, id)
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		t.deliver(rebuilt[id])
	}
	t.settle()
	t.trim()
}

func (t *thread) settle() {
	if !t.rt.known {
		return
	}
	now := t.rt.state()
	ids := make([]uint64, 0, len(t.folds))
	for id, f := range t.folds {
		if !f.ended && now.over(id) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		f := t.folds[id]
		reason := ""
		if id == now.turn && now.state == rpc.RuntimeIdle {
			reason = now.reason
		}
		f.finish(reason)
		next := t.folds[id+1]
		running := next != nil && !next.sealed || id+1 == now.turn && now.state.Busy()
		if id == now.turn {
			running = !now.settled()
		}
		f.idle = !running
		t.deliver(f)
	}
	for s := range t.subs {
		if s.resumed && s.r != nil && t.folds[s.r.cur.Turn] == nil && !t.want[s.r.cur.Turn] {
			if n := s.r.cur.Turn; n > now.turn+1 || n == now.turn+1 && now.settled() {
				s.push(Event{Kind: Failed, Text: fmt.Sprintf("turn %d does not exist; the newest is %d", n, now.turn), Cursor: s.r.cur}, true)
				t.leave(s)
				continue
			}
		}
		if s.r != nil || s.closed {
			continue
		}
		if now.settled() && now.turn <= s.after && t.rt.ver > s.rtVer {
			t.finishWaiting(s, now)
		}
	}
}

func (t *thread) finishWaiting(s *sub, now runState) {
	cur := Cursor{Turn: s.after + 1}
	if s.steer && len(s.replies) == 0 {
		s.push(Event{}, true)
	} else if why, failed := strings.CutPrefix(now.reason, "error:"); failed {
		s.push(Event{Kind: Failed, Text: strings.TrimSpace(why), Cursor: cur}, true)
	} else {
		s.push(Event{Kind: End, Text: joinReplies(s.replies), Cursor: cur}, true)
	}
	t.leave(s)
}

func readTurns(ctx context.Context, conn ariaConn, turn uint64) ([]aria.TurnPart, error) {
	var out []aria.TurnPart
	at := aria.Anchor{Turn: turn}
	for range 64 {
		page, err := conn.Read(ctx, at, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Parts...)
		if !page.More.After || page.Next == nil {
			return out, nil
		}
		at = *page.Next
	}
	return out, errors.New("the thread is longer than 64 pages from that turn")
}

func (t *thread) subscribe(ctx context.Context, s *sub, seed []uint64) <-chan Event {
	s.out = make(chan Event, 64)
	s.wake = make(chan struct{}, 1)
	s.exited = make(chan struct{})
	t.mu.Lock()
	s.rtVer = t.rt.ver
	t.subSeq++
	s.seq = t.subSeq
	t.subs[s] = struct{}{}
	if !t.watching {
		t.watching = true
		go t.watchdog()
	}
	for _, id := range seed {
		if f := t.folds[id]; f == nil {
			t.want[id] = true
		}
	}
	t.startSync()
	t.redialLocked()
	ids := make([]uint64, 0, len(t.folds))
	for id := range t.folds {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		t.offer(s, t.folds[id])
	}
	t.mu.Unlock()
	go t.pump(ctx, s)
	return s.out
}

func (t *thread) deliver(f *turnFold) {
	for s := range t.subs {
		t.offer(s, f)
	}
}

func (t *thread) offer(s *sub, f *turnFold) {
	if s.closed {
		return
	}
	if s.r == nil {
		if f.id <= s.after {
			return
		}
		s.r = newReader(Cursor{Turn: f.id})
		if joinReplies(s.replies) != "" {
			s.r.lead = "\n\n"
		}
	}
	if f.id != s.r.cur.Turn {
		return
	}
	s.r.advance(f, func(e Event) {
		if e.Kind != End {
			s.push(e, e.Kind == Failed)
			return
		}
		s.replies = append(s.replies, e.Text)
		if f.idle || t.claimed(s, f.id) {
			e.Text = joinReplies(s.replies)
			s.push(e, true)
			return
		}
		s.after, s.r = f.id, nil
	})
	if s.closed {
		t.leave(s)
		return
	}
	if s.r == nil {
		if next := t.folds[f.id+1]; next != nil {
			t.offer(s, next)
		}
	}
}

func (t *thread) claimed(by *sub, turn uint64) bool {
	for s := range t.subs {
		if s != by && s.steer && s.r == nil && !s.closed && s.after == turn {
			return true
		}
	}
	return false
}

func joinReplies(rs []string) string {
	var kept []string
	for _, r := range rs {
		if r != "" {
			kept = append(kept, r)
		}
	}
	return strings.Join(kept, "\n\n")
}

func (t *thread) drop(s *sub) {
	t.mu.Lock()
	t.leave(s)
	t.mu.Unlock()
}

func (t *thread) leave(s *sub) {
	delete(t.subs, s)
	if len(t.subs) == 0 {
		t.armIdle()
	}
}

func (t *thread) touch() {
	t.mu.Lock()
	t.lastUsed = time.Now()
	t.armIdle()
	t.mu.Unlock()
}

func (t *thread) armIdle() {
	if len(t.subs) > 0 || t.closed {
		return
	}
	if t.idleTimer == nil {
		t.idleTimer = time.AfterFunc(t.c.IdleClose, t.idleOut)
		return
	}
	t.idleTimer.Reset(t.c.IdleClose)
}

func (t *thread) idleOut() {
	c := t.c
	c.mu.Lock()
	t.mu.Lock()
	idle := len(t.subs) == 0 && !t.closed && time.Since(t.lastUsed) >= c.IdleClose && c.threads[t.id] == t
	if idle {
		delete(c.threads, t.id)
	} else {
		t.armIdle()
	}
	t.mu.Unlock()
	c.mu.Unlock()
	if idle {
		t.shut("")
	}
}

func (c *Client) Forget(threadID string) {
	c.mu.Lock()
	t := c.threads[threadID]
	delete(c.threads, threadID)
	c.mu.Unlock()
	if t != nil {
		t.shut("the thread was forgotten")
	}
}

func (t *thread) pump(ctx context.Context, s *sub) {
	defer close(s.exited)
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.q) == 0 {
			last := s.closed
			s.mu.Unlock()
			if last {
				return
			}
			select {
			case <-s.wake:
			case <-ctx.Done():
				t.drop(s)
				return
			case <-t.c.done:
				return
			}
			continue
		}
		e := s.q[0]
		s.q = s.q[1:]
		s.mu.Unlock()
		select {
		case s.out <- e:
		case <-ctx.Done():
			t.drop(s)
			return
		case <-t.c.done:
			return
		}
	}
}

type sub struct {
	steer   bool
	seq     uint64
	quiet   bool
	quietAt uint64
	resumed bool
	after   uint64
	rtVer   uint64
	r       *reader
	replies []string

	out    chan Event
	wake   chan struct{}
	exited chan struct{}

	mu     sync.Mutex
	q      []Event
	closed bool
}

func (s *sub) next() uint64 {
	if s.r != nil {
		return s.r.cur.Turn
	}
	return s.after + 1
}

func (s *sub) push(e Event, last bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if n := len(s.q); e.Kind == Delta && n > 0 && s.q[n-1].Kind == Delta {
		s.q[n-1].Text += e.Text
		s.q[n-1].Cursor = e.Cursor
	} else if e.Kind != "" {
		s.q = append(s.q, e)
	}
	s.closed = last
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (t *thread) watchdog() {
	tick := time.NewTicker(t.c.CheckEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-t.c.done:
			return
		}
		t.mu.Lock()
		if len(t.subs) == 0 {
			t.watching = false
			t.mu.Unlock()
			return
		}
		conn, gen, seq := t.conn, t.gen, t.subSeq
		waiting := false
		for s := range t.subs {
			waiting = waiting || s.r == nil
		}
		t.mu.Unlock()
		if alive(conn) {
			t.check(conn, gen, seq, waiting)
		}
	}
}

func (t *thread) check(conn ariaConn, gen, seq uint64, waiting bool) {
	ctx, cancel := context.WithTimeout(context.Background(), t.c.CheckEvery)
	defer cancel()
	rt, err := conn.FormOf(ctx, runtimeIntrinsic)
	dormant := isDormant(err)
	if err != nil && !dormant {
		_ = conn.Close()
		return
	}
	queued := -1
	if waiting {
		queued = queueLen(ctx, conn)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen != t.gen {
		return
	}
	if !dormant {
		t.adopt(rt, nil)
	}
	now := t.rt.state()
	if dormant {
		now.state, now.inflight = rpc.RuntimeIdle, 0
	}
	quiet := queued == 0 && now.state == rpc.RuntimeIdle
	for s := range t.subs {
		if s.r == nil && !s.closed && s.seq <= seq && now.turn > s.after && t.folds[s.after+1] == nil {
			t.want[s.after+1] = true
			t.startSync()
		}
		if s.r != nil || s.closed || s.seq > seq || s.after < now.turn || !quiet {
			s.quiet = false
			continue
		}
		if s.quiet && s.quietAt == t.rt.ver {
			t.finishWaiting(s, runState{})
			continue
		}
		s.quiet, s.quietAt = true, t.rt.ver
	}
}

func queueLen(ctx context.Context, conn ariaConn) int {
	q, err := conn.FormOf(ctx, queueIntrinsic)
	if isDormant(err) {
		return 0
	}
	if err != nil {
		return -1
	}
	n := -1
	if raw, ok := q.Snapshot.Get("len"); ok {
		_ = json.Unmarshal(raw, &n)
	}
	return n
}
