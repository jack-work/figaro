package formsim_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"maps"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/form/replica"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/formgen"
	"github.com/jack-work/figaro/internal/store"
)

// The deterministic simulator. The server is the real store.Form; the
// replicas are the real package; only the network and the clock are fake.
// A connection delivers in order and its one fault is dying, which loses
// everything in flight both ways: the "outcome unknown" case. Time runs in a
// synctest bubble, so the replicas' own timers drive the debounce.
//
//	go test ./api/form/replica -run TestSim -sim.seed=N     replay one seed
//	go test ./api/form/replica -run TestSim -sim.n=2000      soak

var (
	simSeed  = flag.Uint64("sim.seed", 0, "run only this seed")
	simN     = flag.Int("sim.n", 200, "seeds to run")
	simSteps = flag.Int("sim.steps", 500, "steps per seed")
	simTrace = flag.Int("sim.trace", 120, "trace lines printed on failure")
)

const (
	debounce = 20 * time.Millisecond
	maxWait  = 100 * time.Millisecond
)

var errLinkDown = errors.New("sim: connection closed")

type upMsg struct {
	set   *rpc.SetRequest
	load  string
	reply chan reply
}

type downMsg struct {
	delta *rpc.FormDelta
	reply chan reply
	ans   reply
}

type reply struct {
	set  *rpc.SetResponse
	load *rpc.FormResponse
	err  error
}

type link struct {
	sim   *sim
	rep   *peer
	alive bool
	up    []upMsg
	down  []downMsg
}

func (l *link) Set(ctx context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	r, err := l.call(ctx, upMsg{set: &req})
	if err != nil {
		return nil, err
	}
	return r.set, r.err
}

func (l *link) Load(ctx context.Context, client string) (*rpc.FormResponse, error) {
	r, err := l.call(ctx, upMsg{load: client})
	if err != nil {
		return nil, err
	}
	return r.load, r.err
}

func (l *link) call(ctx context.Context, m upMsg) (reply, error) {
	m.reply = make(chan reply, 1)
	l.sim.mu.Lock()
	if !l.rep.link.alive || l.rep.link != l {
		l.sim.mu.Unlock()
		return reply{}, errLinkDown
	}
	if m.set != nil {
		l.sim.sent(l.rep, *m.set)
	}
	l.up = append(l.up, m)
	l.sim.mu.Unlock()
	select {
	case r := <-m.reply:
		return r, nil
	case <-ctx.Done():
		return reply{}, ctx.Err()
	}
}

type transport struct{ p *peer }

func (t transport) Set(ctx context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	return t.p.current().Set(ctx, req)
}

func (t transport) Load(ctx context.Context, client string) (*rpc.FormResponse, error) {
	return t.p.current().Load(ctx, client)
}

type change struct{ prev, next form.Snapshot }

type peer struct {
	id      string
	r       *replica.Replica
	link    *link
	gen     *formgen.Gen
	tokens  int
	edits   []edit
	changes []change
	refused map[uint64]int
}

func (p *peer) current() *link {
	p.link.sim.mu.Lock()
	defer p.link.sim.mu.Unlock()
	return p.link
}

type edit struct {
	step   int
	paths  []string
	tokens []string
}

type reqKey struct {
	client string
	seq    uint64
}

// shape is the workload: how many replicas, how narrow the key space, how
// often and how bursty the edits. The zero shape is the default mix.
type shape struct {
	replicas int
	keys     []string
	depth    int
	editW    int
	burst    int
}

type sim struct {
	t     *testing.T
	shape shape
	seed  uint64
	rng   *rand.Rand
	step  int
	trace []string

	mu    sync.Mutex
	srv   *server
	peers []*peer

	history  map[uint64]form.Snapshot
	applied  map[reqKey]uint64
	requests map[reqKey]rpc.SetRequest
	outcomes map[reqKey]string
	lastSent map[string]uint64
	outside  int
	failures []string
}

func newSim(t *testing.T, seed uint64, replicas int, sh shape) *sim {
	if sh.editW == 0 {
		sh.editW = 30
	}
	if sh.burst == 0 {
		sh.burst = 1
	}
	s := &sim{
		t: t, seed: seed, shape: sh,
		rng:      rand.New(rand.NewPCG(seed, 7)),
		history:  map[uint64]form.Snapshot{0: {}},
		applied:  map[reqKey]uint64{},
		requests: map[reqKey]rpc.SetRequest{},
		outcomes: map[reqKey]string{},
		lastSent: map[string]uint64{},
	}
	s.srv = newServer(s.committed)
	for i := 0; i < replicas; i++ {
		p := &peer{id: fmt.Sprintf("r%d", i), gen: formgen.New(seed*31 + uint64(i)), refused: map[uint64]int{}}
		if sh.keys != nil {
			p.gen.Keys, p.gen.Depth = sh.keys, sh.depth
		}
		p.gen.Leaf = func() json.RawMessage {
			p.tokens++
			b, _ := json.Marshal(fmt.Sprintf("%s:%d", p.id, p.tokens))
			return b
		}
		p.link = &link{sim: s, rep: p, alive: true}
		p.r = replica.New(p.id, transport{p}, replica.Options{
			Debounce: debounce,
			MaxWait:  maxWait,
			OnChange: func(prev, next form.Snapshot) {
				s.mu.Lock()
				p.changes = append(p.changes, change{prev, next})
				s.mu.Unlock()
			},
			OnRefused: func(seq uint64, err error) {
				s.mu.Lock()
				p.refused[seq]++
				s.mu.Unlock()
			},
		})
		s.peers = append(s.peers, p)
	}
	return s
}

// committed runs on the form's drainer, inside the server call.
func (s *sim) committed(c commit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.history[c.Version-1]
	if !ok {
		s.fail("commit %d without a recorded state at %d", c.Version, c.Version-1)
		return
	}
	s.history[c.Version] = prev.Apply(c.Patch)
	if c.Origin.Seq > 0 {
		k := reqKey{c.Origin.Client, c.Origin.Seq}
		if at, dup := s.applied[k]; dup {
			s.fail("%s seq %d applied twice: versions %d and %d", k.client, k.seq, at, c.Version)
		}
		s.applied[k] = c.Version
	}
	d := rpc.FormDelta{Schema: rpc.FormDeltaSchema, Version: c.Version, Patch: c.Patch, Client: c.Origin.Client, Seq: c.Origin.Seq}
	for _, p := range s.peers {
		if p.link.alive {
			p.link.down = append(p.link.down, downMsg{delta: &d})
		}
	}
}

func (s *sim) sent(p *peer, req rpc.SetRequest) {
	k := reqKey{p.id, req.Seq}
	if prev, ok := s.requests[k]; ok && formgen.PatchJSON(prev.Patch) != formgen.PatchJSON(req.Patch) {
		s.fail("%s resent seq %d with a different patch", p.id, req.Seq)
	}
	if _, seen := s.requests[k]; !seen && req.Seq <= s.lastSent[p.id] {
		s.fail("%s sent a new seq %d after seq %d", p.id, req.Seq, s.lastSent[p.id])
	}
	s.lastSent[p.id] = max(s.lastSent[p.id], req.Seq)
	s.requests[k] = req
}

func (s *sim) fail(format string, args ...any) {
	s.failures = append(s.failures, fmt.Sprintf("step %d: ", s.step)+fmt.Sprintf(format, args...))
}

func (s *sim) log(format string, args ...any) {
	s.trace = append(s.trace, fmt.Sprintf("%4d ", s.step)+fmt.Sprintf(format, args...))
}

func (s *sim) pick(n int) int { return s.rng.IntN(n) }

func (s *sim) livePeers() []*peer {
	var out []*peer
	for _, p := range s.peers {
		if p.link.alive {
			out = append(out, p)
		}
	}
	return out
}

func (s *sim) edit(p *peer) {
	shown := p.r.Shown()
	var patch form.Patch
	if s.pick(25) == 0 {
		patch = form.Build(shown, map[string]json.RawMessage{"model": p.gen.Leaf()}, nil)
	} else {
		patch = p.gen.Patch(shown)
	}
	if patch.IsIdentity() {
		return
	}
	e := edit{step: s.step}
	for _, en := range patch.Entries() {
		e.paths = append(e.paths, en.Key)
		e.tokens = append(e.tokens, tokensIn(string(en.New), p.id)...)
	}
	p.edits = append(p.edits, e)
	s.log("%s edit %s", p.id, formgen.PatchJSON(patch))
	p.r.Edit(patch)
}

func tokensIn(raw, id string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == '"' }) {
		if strings.HasPrefix(f, id+":") {
			out = append(out, f)
		}
	}
	return out
}

func (s *sim) outsideWrite() {
	s.outside++
	snap, version := s.srv.state()
	tok, _ := json.Marshal(fmt.Sprintf("w:%d", s.outside))
	g := formgen.New(s.seed ^ uint64(s.outside)<<20)
	g.Leaf = func() json.RawMessage { return tok }
	req := rpc.SetRequest{Patch: g.Patch(snap)}
	if s.pick(5) == 0 {
		req.IfVersion = version + 1
	}
	_, err := s.srv.set(req)
	s.log("outside write %s ifVersion=%d err=%v", formgen.PatchJSON(req.Patch), req.IfVersion, err)
}

func (s *sim) deliverUp(p *peer) {
	s.mu.Lock()
	l := p.link
	m := l.up[0]
	l.up = l.up[1:]
	s.mu.Unlock()
	var r reply
	switch {
	case m.set != nil:
		r.set, r.err = s.srv.set(*m.set)
		s.mu.Lock()
		s.outcomes[reqKey{p.id, m.set.Seq}] = describeReply(r)
		s.mu.Unlock()
		s.log("%s up set seq %d -> %s", p.id, m.set.Seq, describeReply(r))
	default:
		r.load = s.srv.load(m.load)
		s.log("%s up load -> v%d lastSeq %d", p.id, r.load.Version, r.load.LastSeq)
	}
	s.mu.Lock()
	if l.alive && m.reply != nil {
		l.down = append(l.down, downMsg{reply: m.reply, ans: r})
	}
	s.mu.Unlock()
}

func describeReply(r reply) string {
	if r.err != nil {
		return "error " + r.err.Error()
	}
	return fmt.Sprintf("%s v%d", r.set.Outcome, r.set.Version)
}

func (s *sim) deliverDown(p *peer) {
	s.mu.Lock()
	m := p.link.down[0]
	p.link.down = p.link.down[1:]
	before := len(p.changes)
	s.mu.Unlock()
	if m.delta != nil {
		own := m.delta.Client == p.id && m.delta.Version == p.r.Version()+1
		s.log("%s down delta v%d (%s seq %d)", p.id, m.delta.Version, m.delta.Client, m.delta.Seq)
		p.r.Delta(*m.delta)
		synctest.Wait()
		if own {
			s.mu.Lock()
			for _, c := range p.changes[before:] {
				if d := c.next.Diff(c.prev); !d.IsIdentity() {
					s.fail("%s flickered when its own seq %d landed: %s", p.id, m.delta.Seq, formgen.PatchJSON(d))
				}
			}
			s.mu.Unlock()
		}
		return
	}
	s.log("%s down reply", p.id)
	m.reply <- m.ans
}

func (s *sim) kill(p *peer) {
	s.mu.Lock()
	l := p.link
	l.alive = false
	waiting := l.up
	var replies []chan reply
	for _, m := range waiting {
		if m.reply != nil {
			replies = append(replies, m.reply)
		}
	}
	for _, m := range l.down {
		if m.reply != nil && m.delta == nil {
			replies = append(replies, m.reply)
		}
	}
	l.up, l.down = nil, nil
	s.mu.Unlock()
	s.log("%s link dies (%d calls lost)", p.id, len(replies))
	for _, ch := range replies {
		ch <- reply{err: errLinkDown}
	}
	synctest.Wait()
	p.r.Lost()
}

func (s *sim) reconnect(p *peer) {
	s.mu.Lock()
	p.link = &link{sim: s, rep: p, alive: true}
	s.mu.Unlock()
	s.log("%s reconnects", p.id)
	p.r.Reload()
}

// timeout answers the oldest call still waiting on a live link with a deadline,
// while the request itself stays in flight: the server may still apply it.
func (s *sim) timeout(p *peer) {
	s.mu.Lock()
	l := p.link
	var ch chan reply
	for i := range l.up {
		if l.up[i].reply != nil {
			ch, l.up[i].reply = l.up[i].reply, nil
			break
		}
	}
	if ch == nil {
		for i := range l.down {
			if l.down[i].delta == nil && l.down[i].reply != nil {
				ch = l.down[i].reply
				l.down = append(l.down[:i:i], l.down[i+1:]...)
				break
			}
		}
	}
	s.mu.Unlock()
	if ch == nil {
		return
	}
	s.log("%s a call times out", p.id)
	ch <- reply{err: context.DeadlineExceeded}
}

// dropDelta loses one notification on a live link: a slow consumer's
// buffer overflowing, the reason a replica must notice gaps.
func (s *sim) dropDelta(p *peer) {
	s.mu.Lock()
	l := p.link
	for i := range l.down {
		if l.down[i].delta != nil {
			s.log("%s loses delta v%d", p.id, l.down[i].delta.Version)
			l.down = append(l.down[:i:i], l.down[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

func (s *sim) crash() {
	s.log("server crash")
	for _, p := range s.livePeers() {
		s.kill(p)
	}
	s.srv.crash()
}

func (s *sim) advance(d time.Duration) {
	s.log("time +%s", d)
	time.Sleep(d)
}

func (s *sim) stepOnce() {
	type action struct {
		w int
		f func()
	}
	var acts []action
	live := s.livePeers()
	if len(live) > 0 {
		p := live[s.pick(len(live))]
		acts = append(acts, action{s.shape.editW, func() {
			for n := 1 + s.pick(s.shape.burst); n > 0; n-- {
				s.edit(p)
				synctest.Wait()
			}
		}})
	}
	s.mu.Lock()
	var ups, downs, dead []*peer
	for _, p := range s.peers {
		if len(p.link.up) > 0 {
			ups = append(ups, p)
		}
		if len(p.link.down) > 0 {
			downs = append(downs, p)
		}
		if !p.link.alive {
			dead = append(dead, p)
		}
	}
	s.mu.Unlock()
	if len(ups) > 0 {
		p := ups[s.pick(len(ups))]
		acts = append(acts, action{25, func() { s.deliverUp(p) }})
	}
	if len(downs) > 0 {
		p := downs[s.pick(len(downs))]
		acts = append(acts, action{25, func() { s.deliverDown(p) }})
	}
	acts = append(acts, action{10, func() { s.advance(time.Duration(1+s.pick(int(2*debounce/time.Millisecond))) * time.Millisecond) }})
	acts = append(acts, action{3, s.outsideWrite})
	if len(live) > 0 {
		p := live[s.pick(len(live))]
		acts = append(acts, action{2, func() { s.kill(p) }})
	}
	if len(ups)+len(downs) > 0 {
		busy := append(append([]*peer(nil), ups...), downs...)
		p := busy[s.pick(len(busy))]
		acts = append(acts, action{2, func() { s.timeout(p) }})
	}
	if len(downs) > 0 {
		p := downs[s.pick(len(downs))]
		acts = append(acts, action{1, func() { s.dropDelta(p) }})
	}
	if len(dead) > 0 {
		p := dead[s.pick(len(dead))]
		acts = append(acts, action{6, func() { s.reconnect(p) }})
	}
	acts = append(acts, action{1, s.crash})
	total := 0
	for _, a := range acts {
		total += a.w
	}
	n := s.pick(total)
	for _, a := range acts {
		if n < a.w {
			a.f()
			break
		}
		n -= a.w
	}
	synctest.Wait()
}

// Checked after every step. Confirmed and the pending queue are the
// replica's own; what shows from outside is the version Shown is built on,
// and that with nothing pending Shown is exactly the server at that version.
func (s *sim) checkStep() {
	for _, p := range s.peers {
		v := p.r.Version()
		want, ok := s.history[v]
		switch {
		case !ok:
			s.fail("%s built on v%d, a version the server never had", p.id, v)
		case p.r.Pending() == 0 && !formgen.Same(p.r.Shown(), want):
			s.fail("%s has nothing pending at v%d but shows something else\n  replica %s\n  server  %s", p.id, v, formgen.JSON(p.r.Shown()), formgen.JSON(want))
		}
	}
}

func (s *sim) quiesce() {
	s.log("quiesce")
	for _, p := range s.peers {
		if !p.link.alive {
			s.reconnect(p)
		}
	}
	for round := 0; round < 1000; round++ {
		time.Sleep(maxWait + debounce)
		synctest.Wait()
		moved := false
		for _, p := range s.peers {
			for {
				s.mu.Lock()
				nu, nd := len(p.link.up), len(p.link.down)
				s.mu.Unlock()
				if nu == 0 && nd == 0 {
					break
				}
				moved = true
				if nu > 0 {
					s.deliverUp(p)
				} else {
					s.deliverDown(p)
				}
				synctest.Wait()
			}
		}
		if !moved {
			// Quiet for one round is not quiet: a replica may be backing off
			// a failed load for seconds. Wait out the longest backoff and the
			// request timeout before believing it.
			time.Sleep(15 * time.Second)
			synctest.Wait()
			if s.idle() {
				return
			}
		}
	}
	s.fail("no quiescence after 1000 rounds")
}

func (s *sim) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if len(p.link.up) > 0 || len(p.link.down) > 0 {
			return false
		}
	}
	return true
}

// Checked once nothing is in flight.
func (s *sim) checkQuiet() {
	server, version := s.srv.state()
	if want := s.history[version]; !formgen.Same(server, want) {
		s.fail("server state at v%d differs from its own history\n  form    %s\n  history %s", version, formgen.JSON(server), formgen.JSON(want))
	}
	for _, p := range s.peers {
		if p.r.Version() != version || !formgen.Same(p.r.Shown(), server) {
			s.fail("%s did not converge: v%d %s, server v%d %s", p.id, p.r.Version(), formgen.JSON(p.r.Shown()), version, formgen.JSON(server))
		}
		if n := p.r.Pending(); n > 0 {
			s.fail("%s still holds %d unanswered edits", p.id, n)
		}
		s.mu.Lock()
		refused := maps.Clone(p.refused)
		s.mu.Unlock()
		for seq, n := range refused {
			if n > 1 {
				s.fail("%s seq %d refused %d times", p.id, seq, n)
			}
			if at, ok := s.applied[reqKey{p.id, seq}]; ok {
				s.fail("%s seq %d both refused and applied at v%d", p.id, seq, at)
			}
		}
	}
	for k := range s.requests {
		if _, ok := s.outcomes[k]; !ok {
			s.fail("%s seq %d was sent, never reached the server, and is no longer pending: lost", k.client, k.seq)
		}
	}
	s.checkProvenance(server)
}

// Every token an edit wrote either reached the server in some request, or a
// later edit of the same replica touched its path first. A refused request's
// tokens appear nowhere.
func (s *sim) checkProvenance(server form.Snapshot) {
	sentTokens := map[string]reqKey{}
	for k, req := range s.requests {
		for _, en := range req.Patch.Entries() {
			for _, tok := range tokensIn(string(en.New), k.client) {
				sentTokens[tok] = k
			}
		}
	}
	appliedBy := map[string]reqKey{}
	for k := range s.applied {
		for _, en := range s.requests[k].Patch.Entries() {
			for _, tok := range tokensIn(string(en.New), k.client) {
				if other, dup := appliedBy[tok]; dup && other != k {
					s.fail("token %s applied under two requests: %s seq %d and seq %d", tok, k.client, other.seq, k.seq)
				}
				appliedBy[tok] = k
			}
		}
	}
	serverJSON := formgen.JSON(server)
	for _, p := range s.peers {
		shownJSON := formgen.JSON(p.r.Shown())
		for i, e := range p.edits {
			for _, tok := range e.tokens {
				k, sent := sentTokens[tok]
				if !sent {
					if !touchedLater(p.edits[i+1:], e.paths) {
						s.fail("%s lost an edit: token %s (step %d, paths %v) was never sent and nothing replaced it", p.id, tok, e.step, e.paths)
					}
					continue
				}
				if p.refused[k.seq] > 0 && (strings.Contains(serverJSON, `"`+tok+`"`) || strings.Contains(shownJSON, `"`+tok+`"`)) {
					s.fail("%s refused seq %d still shows token %s", p.id, k.seq, tok)
				}
			}
		}
	}
}

func touchedLater(later []edit, paths []string) bool {
	for _, e := range later {
		for _, a := range e.paths {
			for _, b := range paths {
				if a == b || strings.HasPrefix(a, b+".") || strings.HasPrefix(b, a+".") {
					return true
				}
			}
		}
	}
	return false
}

func (s *sim) close() {
	for _, p := range s.livePeers() {
		s.kill(p)
	}
	synctest.Wait()
	for _, p := range s.peers {
		p.r.Close()
	}
	s.srv.close()
	time.Sleep(time.Minute)
	synctest.Wait()
}

func (s *sim) digest() uint64 {
	h := fnv.New64a()
	for _, line := range s.trace {
		h.Write([]byte(line))
		h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

func (s *sim) report() {
	if len(s.failures) == 0 {
		return
	}
	from := max(0, len(s.trace)-*simTrace)
	s.t.Errorf("seed %d: %d invariant failures; replay with -sim.seed=%d\n%s\n--- trace (last %d of %d steps) ---\n%s",
		s.seed, len(s.failures), s.seed,
		strings.Join(s.failures[:min(len(s.failures), 10)], "\n"),
		len(s.trace)-from, len(s.trace), strings.Join(s.trace[from:], "\n"))
}

func runSim(t *testing.T, seed uint64, steps int) uint64 { return runShape(t, seed, steps, shape{}) }

func runShape(t *testing.T, seed uint64, steps int, sh shape) uint64 {
	var digest uint64
	synctest.Test(t, func(t *testing.T) {
		n := sh.replicas
		if n == 0 {
			n = 2 + int(seed%3)
		}
		s := newSim(t, seed, n, sh)
		synctest.Wait()
		for s.step = 1; s.step <= steps && len(s.failures) == 0; s.step++ {
			s.stepOnce()
			s.checkStep()
		}
		if len(s.failures) == 0 {
			s.quiesce()
			s.checkQuiet()
		}
		digest = s.digest()
		s.report()
		s.close()
	})
	return digest
}

func seeds() []uint64 {
	if *simSeed != 0 {
		return []uint64{*simSeed}
	}
	out := make([]uint64, *simN)
	for i := range out {
		out[i] = uint64(i + 1)
	}
	return out
}

func TestSim(t *testing.T) {
	for _, seed := range seeds() {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runSim(t, seed, *simSteps)
		})
	}
}

// A seed is a replay only if the same seed walks the same steps.
func TestSimDeterministic(t *testing.T) {
	for _, seed := range []uint64{3, 17, 101} {
		for _, sh := range []shape{{}, {replicas: 8, keys: []string{"a", "b"}, depth: 2, editW: 60, burst: 5}} {
			a := runShape(t, seed, 200, sh)
			b := runShape(t, seed, 200, sh)
			if a != b {
				t.Errorf("seed %d, shape %+v: two runs took different paths (%x vs %x)", seed, sh, a, b)
			}
		}
	}
}

// A sanity check on the oracle itself: the history the simulator keeps folds
// to the same state the server holds, on a run with no replicas at all.
func TestSimOracleTracksTheServer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSim(t, 9, 0, shape{})
		for i := 0; i < 300; i++ {
			s.step = i
			s.outsideWrite()
			if s.pick(40) == 0 {
				s.crash()
			}
		}
		synctest.Wait()
		server, version := s.srv.state()
		if !formgen.Same(server, s.history[version]) {
			t.Fatalf("oracle drifted at v%d\n  form    %s\n  history %s", version, formgen.JSON(server), formgen.JSON(s.history[version]))
		}
		keys := make([]int, 0, len(s.history))
		for v := range s.history {
			keys = append(keys, int(v))
		}
		sort.Ints(keys)
		for i, v := range keys {
			if v != i {
				t.Fatalf("versions not consecutive: %v", keys[:i+1])
			}
		}
		s.close()
	})
}

// A paged log rebases its in-memory window every few records, and a crash
// reopens from that base: whatever the server keeps per client must survive
// a reopen that no longer replays the records that set it.
func TestSimSmallPages(t *testing.T) {
	store.SetMemFormPage(8)
	t.Cleanup(func() { store.SetMemFormPage(512) })
	n := *simN
	if *simSeed != 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		seed := uint64(i + 1)
		if *simSeed != 0 {
			seed = *simSeed
		}
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runSim(t, seed, *simSteps)
		})
	}
}

// Eight replicas editing two keys at depth two in bursts: nearly every edit
// collides with another replica's, and drafts merge and split constantly.
func TestSimConflicts(t *testing.T) {
	sh := shape{replicas: 8, keys: []string{"a", "b"}, depth: 2, editW: 60, burst: 5}
	for _, seed := range seeds() {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			runShape(t, seed, *simSteps, sh)
		})
	}
}
