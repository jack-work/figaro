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
)

type Client struct {
	ep transport.Endpoint

	ReconnectFor time.Duration

	mu      sync.Mutex
	angelus *sdk.Angelus
	threads map[string]*thread
	closed  bool
}

func New(ep transport.Endpoint) *Client {
	return &Client{ep: ep, ReconnectFor: 30 * time.Second, threads: map[string]*thread{}}
}

var ErrClosed = errors.New("chat: client closed")

func (c *Client) Close() error {
	c.mu.Lock()
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

func (c *Client) withAngelus(ctx context.Context, fn func(*sdk.Angelus) error) error {
	err := c.tryAngelus(fn)
	if err == nil || ctx.Err() != nil || errors.Is(err, ErrClosed) {
		return err
	}
	c.mu.Lock()
	if c.angelus != nil {
		_ = c.angelus.Close()
		c.angelus = nil
	}
	c.mu.Unlock()
	return c.tryAngelus(fn)
}

func (c *Client) tryAngelus(fn func(*sdk.Angelus) error) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.angelus == nil {
		a, err := sdk.DialAngelus(c.ep)
		if err != nil {
			c.mu.Unlock()
			return fmt.Errorf("dial figaro: %w", err)
		}
		c.angelus = a
	}
	a := c.angelus
	c.mu.Unlock()
	return fn(a)
}

func (c *Client) Open(ctx context.Context, root string) (string, error) {
	var id string
	err := c.withAngelus(ctx, func(a *sdk.Angelus) error {
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
	if active {
		return nil, nil
	}
	return t.subscribe(ctx, &sub{after: uint64(cursor)}, nil), nil
}

func (c *Client) Resume(ctx context.Context, threadID string, from Cursor) (<-chan Event, error) {
	if from.Turn == 0 {
		return nil, errors.New("chat: resume needs a cursor with a turn")
	}
	t, _, err := c.thread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	return t.subscribe(ctx, &sub{after: from.Turn - 1, r: newReader(from)}, []uint64{from.Turn}), nil
}

func (c *Client) Cancel(ctx context.Context, threadID string) error {
	_, conn, err := c.thread(ctx, threadID)
	if err != nil {
		return err
	}
	return conn.Interrupt(ctx)
}

func (c *Client) thread(ctx context.Context, id string) (*thread, *sdk.Aria, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClosed
	}
	t := c.threads[id]
	if t == nil {
		t = &thread{c: c, id: id, folds: map[uint64]*turnFold{}, want: map[uint64]bool{}, subs: map[*sub]struct{}{}}
		c.threads[id] = t
	}
	c.mu.Unlock()
	conn, err := t.connect(ctx)
	return t, conn, err
}

type note struct {
	part *aria.TurnPart
	done *rpc.DoneEntry
}

type thread struct {
	c  *Client
	id string

	dial sync.Mutex

	mu      sync.Mutex
	conn    *sdk.Aria
	folds   map[uint64]*turnFold
	top     uint64
	syncing bool
	want    map[uint64]bool
	buf     []note
	subs    map[*sub]struct{}
}

func (t *thread) connect(ctx context.Context) (*sdk.Aria, error) {
	t.dial.Lock()
	defer t.dial.Unlock()
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn != nil {
		select {
		case <-conn.Done():
		default:
			return conn, nil
		}
	}
	var canonical string
	if err := t.c.withAngelus(ctx, func(a *sdk.Angelus) error {
		att, err := a.Attach(ctx, t.id)
		if err != nil {
			return fmt.Errorf("attach %s: %w", t.id, err)
		}
		canonical = att.FigaroID
		return nil
	}); err != nil {
		return nil, err
	}
	conn, err := sdk.DialAria(sdk.Addr{Door: t.c.ep, ID: canonical}, t.notify)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", t.id, err)
	}
	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	go t.watch(conn)
	return conn, nil
}

func (t *thread) watch(conn *sdk.Aria) {
	<-conn.Done()
	t.mu.Lock()
	if t.conn != conn {
		t.mu.Unlock()
		return
	}
	t.conn = nil
	t.folds = map[uint64]*turnFold{}
	listening := len(t.subs) > 0
	t.mu.Unlock()
	if listening {
		t.reconnect()
	}
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
			for s := range t.subs {
				t.want[s.next()] = true
			}
			t.startSync()
			t.mu.Unlock()
			return
		}
		if errors.Is(err, ErrClosed) || time.Now().Add(wait).After(deadline) {
			t.fail("lost the connection to figaro: " + err.Error())
			return
		}
		time.Sleep(wait)
		wait = min(2*wait, 2*time.Second)
	}
}

func (t *thread) shut(why string) {
	t.fail(why)
	t.mu.Lock()
	conn := t.conn
	t.conn = nil
	t.mu.Unlock()
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
		delete(t.subs, s)
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
	case rpc.MethodTurnDone:
		var d rpc.DoneEntry
		_ = json.Unmarshal(params, &d)
		notes = append(notes, note{done: &d})
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
	if n.done != nil {
		f := t.folds[t.top]
		if f == nil || f.ended {
			return true
		}
		f.finish(n.done.Reason)
		f.idle = n.done.Idle == nil || *n.done.Idle
		t.deliver(f)
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
		for old := range t.folds {
			if old+keptTurns <= id {
				delete(t.folds, old)
			}
		}
	}
	return f
}

const keptTurns = 4

func (t *thread) startSync() {
	if t.syncing || len(t.want) == 0 {
		return
	}
	t.syncing = true
	go t.sync()
}

func (t *thread) sync() {
	for {
		t.mu.Lock()
		if len(t.want) > 0 {
			turns := make([]uint64, 0, len(t.want))
			for id := range t.want {
				turns = append(turns, id)
			}
			t.want = map[uint64]bool{}
			conn := t.conn
			t.mu.Unlock()
			t.seed(conn, turns)
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

func (t *thread) seed(conn *sdk.Aria, turns []uint64) {
	if conn == nil {
		return
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i] < turns[j] })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	parts, err := readTurns(ctx, conn, turns[0])
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		for s := range t.subs {
			if s.r != nil && s.r.cur.Turn >= turns[0] || s.r == nil && s.after+1 >= turns[0] {
				s.push(Event{Kind: Failed, Text: "read the thread: " + err.Error(), Cursor: Cursor{Turn: s.next()}}, true)
				delete(t.subs, s)
			}
		}
		return
	}
	rebuilt := map[uint64]*turnFold{}
	var last uint64
	for _, p := range parts {
		f := rebuilt[p.ID]
		if f == nil {
			f = newTurnFold(p.ID)
			rebuilt[p.ID] = f
		}
		f.apply(p)
		last = max(last, p.ID)
	}
	for id, f := range rebuilt {
		if old := t.folds[id]; old != nil && old.ended && !f.sealed {
			continue
		}
		if f.sealed {
			f.finish("")
			f.idle = id == last
		}
		t.folds[id] = f
		t.fold(id)
	}
	ids := make([]uint64, 0, len(rebuilt))
	for id := range rebuilt {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		t.deliver(t.folds[id])
	}
}

func readTurns(ctx context.Context, conn *sdk.Aria, turn uint64) ([]aria.TurnPart, error) {
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
	t.mu.Lock()
	t.subs[s] = struct{}{}
	for _, id := range seed {
		if f := t.folds[id]; f == nil {
			t.want[id] = true
		}
	}
	t.startSync()
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
		if len(s.replies) > 0 && s.replies[len(s.replies)-1] != "" {
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
		if f.idle {
			e.Text = joinReplies(s.replies)
			s.push(e, true)
			return
		}
		s.after, s.r = f.id, nil
	})
	if s.closed {
		delete(t.subs, s)
		return
	}
	if s.r == nil {
		if next := t.folds[f.id+1]; next != nil {
			t.offer(s, next)
		}
	}
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
	delete(t.subs, s)
	t.mu.Unlock()
}

func (t *thread) pump(ctx context.Context, s *sub) {
	defer close(s.out)
	for {
		select {
		case <-s.wake:
		case <-ctx.Done():
			t.drop(s)
			return
		}
		s.mu.Lock()
		batch, last := s.q, s.closed
		s.q = nil
		s.mu.Unlock()
		for _, e := range batch {
			select {
			case s.out <- e:
			case <-ctx.Done():
				t.drop(s)
				return
			}
		}
		if last {
			return
		}
	}
}

type sub struct {
	after   uint64
	r       *reader
	replies []string

	out  chan Event
	wake chan struct{}

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
	s.q = append(s.q, e)
	s.closed = last
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
