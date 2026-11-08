package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

// nil writer, nil tap: the non-recording path does not wrap the conn.
func tapeTap(w *tape.Writer) transport.Tap {
	if w == nil {
		return nil
	}
	return func(c net.Conn) net.Conn { return tape.Tap(c, w) }
}

type replayOpts struct {
	speed    float64
	summary  bool
	lockstep string
	fps      int
	animFPS  int
}

func runReplay(loaded *config.Loaded, path string, opt replayOpts) {
	h, frames, err := tape.Read(path)
	if err != nil {
		die("%s", err)
	}
	if opt.summary {
		printTapeSummary(os.Stdout, h, frames)
		return
	}
	if len(frames) == 0 {
		die("tape %s has no frames", path)
	}

	dir, err := os.MkdirTemp("", "figtape-")
	if err != nil {
		die("replay: %s", err)
	}
	removeDir := sync.OnceFunc(func() { os.RemoveAll(dir) })
	atExit(removeDir)
	defer removeDir()
	sock := filepath.Join(dir, "agent.sock")
	ep := transport.UnixEndpoint(sock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started, _ := time.Parse(time.RFC3339Nano, h.Started)
	var driver net.Conn
	if opt.lockstep != "" {
		if driver, err = acceptLockstepDriver(opt.lockstep); err != nil {
			die("replay --lockstep: %s", err)
		}
	}

	srv := &tapeServer{frames: frames, speed: opt.speed, done: make(chan struct{})}
	end := make(chan struct{})
	if driver != nil {
		ls, err := startLockstep(ctx, h, srv, driver, opt.fps, opt.animFPS, started)
		if err != nil {
			driver.Close()
			die("replay --lockstep: %s", err)
		}
		atExit(ls.close)
		defer ls.close()
		end = ls.end
		if os.Getenv("FIGARO_AMBIGUOUS_WIDE") == "" {
			os.Setenv("FIGARO_AMBIGUOUS_WIDE", "0")
		}
		applyAmbiguousWidth()
	}
	if err := srv.listen(ctx, ep); err != nil {
		die("replay: %s", err)
	}
	// The renderer leaves through tailFigaro's end-of-stream door, not ctx
	// cancellation, which would report an interrupt that never happened.
	if driver == nil {
		go func() {
			<-srv.done
			time.Sleep(300 * time.Millisecond) // let the last frame paint
			close(end)
		}()
	}

	id := h.Aria
	if id == "" {
		id = "tape"
	}
	window := 0
	if h.Model != nil {
		window = h.Model.ContextWindow
	}
	sessionNow = newTapeClock(started, frames, srv.position).now
	tapeOnly.Store(true)
	tailFigaro(ctx, cancel, sdk.Addr{Door: ep, ID: id}, h.Aria, loaded, tailOpts{end: end, startedAt: started, window: window})
}

func printTapeSummary(w *os.File, h tape.Header, frames []tape.Frame) {
	fmt.Fprintf(w, "tape v%d · aria %s · %d frames\n", h.Tape, h.Aria, len(frames))
	fmt.Fprintf(w, "  recorded  %s by %s\n", h.Started, h.Binary)
	fmt.Fprintf(w, "  geometry  %dx%d %s\n", h.Cols, h.Rows, h.Term)
	if h.Command != "" {
		fmt.Fprintf(w, "  command   %s\n", h.Command)
	}
	if h.Note != "" {
		fmt.Fprintf(w, "  note      %s\n", h.Note)
	}
	if len(frames) == 0 {
		return
	}
	byMethod := map[string]int{}
	for _, f := range frames {
		key := string(f.Dir) + " " + f.Method()
		if f.Method() == "" {
			key = string(f.Dir) + " <response>"
		}
		byMethod[key]++
	}
	fmt.Fprintf(w, "  duration  %s\n", frames[len(frames)-1].At().Round(time.Millisecond))
	for k, n := range byMethod {
		fmt.Fprintf(w, "  %-22s %d\n", k, n)
	}
}

type tapeServer struct {
	frames []tape.Frame
	speed  float64
	done   chan struct{}
	closed bool

	mu        sync.Mutex
	start     time.Time
	sent      time.Duration
	metrics   json.RawMessage
	metricsAt time.Duration
	sides     []*jkrpc.Server

	stepped func() time.Duration
	main    *jkrpc.Server
	ready   chan struct{}
	cursor  int
}

func (s *tapeServer) listen(ctx context.Context, ep transport.Endpoint) error {
	ln, err := transport.Listen(ep)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		answers := recordedAnswers(s.frames)
		for first := true; ; first = false {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			srv := jkrpc.NewServer(jkrpc.NewConn(conn), s.handlers(answers))
			go srv.Serve(ctx)
			if first && s.stepped != nil {
				s.mu.Lock()
				s.main = srv
				s.mu.Unlock()
				close(s.ready)
				continue
			}
			if first {
				go s.serve(ctx, srv)
				continue
			}
			s.mu.Lock()
			s.sides = append(s.sides, srv)
			s.mu.Unlock()
		}
	}()
	return nil
}

func (s *tapeServer) handlers(answers tapeAnswers) map[string]jkrpc.HandlerFunc {
	handlers := map[string]jkrpc.HandlerFunc{}
	for _, m := range []string{
		rpc.MethodRead, rpc.MethodQueued, rpc.MethodForm, rpc.MethodContext,
		rpc.MethodInterrupt, rpc.MethodSet, rpc.MethodQua,
		rpc.MethodQueueUpdate, rpc.MethodQueueDelete, rpc.MethodFollow,
	} {
		method := m
		handlers[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			ans, ok := answers.at(method, params, s.position())
			if !ok {
				return nil, nil
			}
			return s.withNotifiedMetrics(ans), nil
		}
	}
	return handlers
}

func (s *tapeServer) serve(ctx context.Context, srv *jkrpc.Server) {
	s.mu.Lock()
	s.start = time.Now()
	s.mu.Unlock()
	for _, f := range s.frames {
		if f.Dir != tape.In || f.Method() == "" {
			continue
		}
		if err := s.waitUntil(ctx, s.start, f.At()); err != nil {
			break
		}
		if !s.push(srv, f) {
			break
		}
	}
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}

func (s *tapeServer) push(srv *jkrpc.Server, f tape.Frame) bool {
	var m struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(f.Msg, &m) != nil {
		return true
	}
	var carried struct {
		Metrics json.RawMessage `json:"metrics"`
	}
	_ = json.Unmarshal(m.Params, &carried)
	s.mu.Lock()
	if f.At() > s.sent {
		s.sent = f.At()
	}
	if len(carried.Metrics) > 0 && string(carried.Metrics) != "null" {
		s.metrics, s.metricsAt = carried.Metrics, f.At()
	}
	var sides []*jkrpc.Server
	if m.Method == rpc.MethodFormDelta {
		sides = s.sides
	}
	s.mu.Unlock()
	if srv.Notify(m.Method, m.Params) != nil {
		return false
	}
	for _, side := range sides {
		_ = side.Notify(m.Method, m.Params)
	}
	return true
}

func (s *tapeServer) nextAt() (time.Duration, bool) {
	for ; s.cursor < len(s.frames); s.cursor++ {
		if f := s.frames[s.cursor]; f.Dir == tape.In && f.Method() != "" {
			return f.At(), true
		}
	}
	return 0, false
}

func (s *tapeServer) pushThrough(bound time.Duration, at func(time.Duration)) bool {
	var due []tape.Frame
	for i := s.cursor; i < len(s.frames); i++ {
		f := s.frames[i]
		if f.Dir != tape.In || f.Method() == "" {
			continue
		}
		if f.At() > bound {
			break
		}
		due = append(due, f)
		s.cursor = i + 1
	}
	if len(due) == 0 {
		return true
	}
	at(due[len(due)-1].At())
	for _, f := range due {
		if !s.push(s.main, f) {
			return false
		}
	}
	return true
}

func (s *tapeServer) position() time.Duration {
	if s.stepped != nil {
		return s.stepped()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.speed <= 0 || s.start.IsZero() {
		return s.sent
	}
	if played := time.Duration(float64(time.Since(s.start)) * s.speed); played > s.sent {
		return played
	}
	return s.sent
}

func (s *tapeServer) withNotifiedMetrics(ans recordedAnswer) json.RawMessage {
	s.mu.Lock()
	fresh, at := s.metrics, s.metricsAt
	s.mu.Unlock()
	if fresh == nil || at < ans.asked {
		return ans.result
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(ans.result, &fields) != nil {
		return ans.result
	}
	if _, ok := fields["metrics"]; !ok {
		return ans.result
	}
	fields["metrics"] = fresh
	out, err := json.Marshal(fields)
	if err != nil {
		return ans.result
	}
	return out
}

type tapeClock struct {
	anchors  []clockAnchor
	position func() time.Duration
}

type clockAnchor struct {
	t    time.Duration
	wall int64
}

func newTapeClock(started time.Time, frames []tape.Frame, position func() time.Duration) tapeClock {
	c := tapeClock{position: position}
	if !started.IsZero() {
		c.anchors = append(c.anchors, clockAnchor{wall: started.UnixMilli()})
	}
	for _, f := range frames {
		if f.Dir != tape.In || f.Method() != rpc.MethodFormDelta {
			continue
		}
		var m struct {
			Params struct {
				At int64 `json:"at"`
			} `json:"params"`
		}
		if json.Unmarshal(f.Msg, &m) != nil || m.Params.At <= 0 {
			continue
		}
		a := clockAnchor{t: f.At(), wall: m.Params.At}
		if n := len(c.anchors); n > 0 {
			last := &c.anchors[n-1]
			if a.wall <= last.wall {
				continue
			}
			if a.t <= last.t {
				last.wall = a.wall
				continue
			}
		}
		c.anchors = append(c.anchors, a)
	}
	return c
}

func (c tapeClock) now() time.Time { return c.wallAt(c.position()) }

func (c tapeClock) wallAt(t time.Duration) time.Time {
	if len(c.anchors) == 0 {
		return time.Now()
	}
	i := sort.Search(len(c.anchors), func(i int) bool { return c.anchors[i].t > t })
	switch {
	case i == 0:
		a := c.anchors[0]
		return time.UnixMilli(a.wall).Add(t - a.t)
	case i == len(c.anchors):
		a := c.anchors[i-1]
		return time.UnixMilli(a.wall).Add(t - a.t)
	}
	a, b := c.anchors[i-1], c.anchors[i]
	frac := float64(t-a.t) / float64(b.t-a.t)
	return time.UnixMilli(a.wall + int64(frac*float64(b.wall-a.wall)))
}

type recordedAnswer struct {
	asked  time.Duration
	result json.RawMessage
}

type tapeAnswers struct {
	byAsk    map[string][]recordedAnswer
	byMethod map[string][]recordedAnswer
}

func recordedAnswers(frames []tape.Frame) tapeAnswers {
	a := tapeAnswers{byAsk: map[string][]recordedAnswer{}, byMethod: map[string][]recordedAnswer{}}
	for i, f := range frames {
		method := f.Method()
		if f.Dir != tape.Out || method == "" {
			continue
		}
		id := f.ID()
		for _, g := range frames[i:] {
			if g.Dir != tape.In || g.ID() != id || g.Method() != "" {
				continue
			}
			var m struct {
				Result json.RawMessage `json:"result"`
			}
			_ = json.Unmarshal(g.Msg, &m)
			var req struct {
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(f.Msg, &req)
			r := recordedAnswer{asked: f.At(), result: m.Result}
			key := askKey(method, req.Params)
			a.byAsk[key] = append(a.byAsk[key], r)
			a.byMethod[method] = append(a.byMethod[method], r)
			break
		}
	}
	return a
}

func (a tapeAnswers) at(method string, params json.RawMessage, now time.Duration) (recordedAnswer, bool) {
	rs := a.byAsk[askKey(method, params)]
	if len(rs) == 0 {
		rs = a.byMethod[method]
	}
	if len(rs) == 0 {
		return recordedAnswer{}, false
	}
	pick := rs[0]
	for _, r := range rs {
		if r.asked > now {
			break
		}
		pick = r
	}
	return pick, pick.result != nil
}

func askKey(method string, params json.RawMessage) string {
	var p map[string]json.RawMessage
	if json.Unmarshal(params, &p) != nil {
		return method + " " + string(params)
	}
	delete(p, "x-caller")
	delete(p, "client")
	canon, _ := json.Marshal(p)
	return method + " " + string(canon)
}

// speed <= 0 plays as fast as the client will take frames.
func (s *tapeServer) waitUntil(ctx context.Context, start time.Time, at time.Duration) error {
	if s.speed <= 0 {
		return ctx.Err()
	}
	due := start.Add(time.Duration(float64(at) / s.speed))
	d := time.Until(due)
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

const recordModelWait = 3 * time.Second

func recordedModel(ctx context.Context, acli *sdk.Angelus, ep sdk.Addr) *tape.Model {
	ctx, cancel := context.WithTimeout(ctx, recordModelWait)
	defer cancel()
	bound, err := readForm(ctx, ep)
	if err != nil {
		return nil
	}
	provider, name := models.Subject(bound)
	catalogAt := sdk.Addr{Door: ep.Door, ID: models.FormID}
	catalog, _ := readForm(ctx, catalogAt)
	limit := models.ContextLimit(catalog, bound)
	if limit <= 0 && provider != "" && name != "" && acli != nil && acli.ModelsWant(ctx, provider, name) == nil {
		wait := time.NewTicker(100 * time.Millisecond)
		defer wait.Stop()
		for limit <= 0 && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-wait.C:
				catalog, _ = readForm(ctx, catalogAt)
				limit = models.ContextLimit(catalog, bound)
			}
		}
	}
	return modelEntry(catalog, provider, name, limit)
}

func modelEntry(catalog form.Snapshot, provider, name string, limit int) *tape.Model {
	if name == "" && limit <= 0 {
		return nil
	}
	e, _ := models.Lookup(catalog, provider, name)
	return &tape.Model{Provider: provider, Name: name, ContextWindow: limit, MaxOutput: e.MaxOutput}
}

func readForm(ctx context.Context, ep sdk.Addr) (form.Snapshot, error) {
	c, err := sdk.DialAria(ep, nil)
	if err != nil {
		return form.Snapshot{}, err
	}
	defer c.Close()
	resp, err := c.Form(ctx)
	if err != nil {
		return form.Snapshot{}, err
	}
	return resp.Snapshot, nil
}
