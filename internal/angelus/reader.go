package angelus

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

// AriaReader answers history, context and form from the store, with no agent.
type AriaReader struct {
	backend store.Backend
	proj    Projector
	uiCache *aria.ComposedCache
	models  *modelcat.Catalog

	// servers share the agents' TurnCache and budget; one read path, not two.
	mu         sync.Mutex
	servers    map[string]*readAria
	forkedFrom map[string]string

	// liveTurns is a live agent's turn server, nil when none is resident.
	liveTurns func(id string) *aria.Server
}

// readAria caches metrics from its last refresh; the log growing is what
// triggers both. mu makes one build per aria: concurrent readers wait for it.
type readAria struct {
	mu      sync.Mutex
	srv     *aria.Server
	metrics *aria.Metrics
}

// Projector is declared here so the reader does not depend on the agent package.
type Projector interface {
	Turns(msgs []message.Message) []aria.Turn
}

func NewAriaReader(backend store.Backend, proj Projector) *AriaReader {
	return NewAriaReaderBounded(backend, proj, nil)
}

func NewAriaReaderBounded(backend store.Backend, proj Projector, uiCache *aria.ComposedCache) *AriaReader {
	return &AriaReader{backend: backend, proj: proj, uiCache: uiCache, servers: map[string]*readAria{}, forkedFrom: map[string]string{}}
}

// messages also returns the entries: they carry the cursor stamps the
// payload does not, and form-delta assembly reads them.
func (r *AriaReader) messages(id string) ([]message.Message, []arialog.Entry[message.Message], error) {
	if r == nil || r.backend == nil {
		return nil, nil, errors.New("no backend")
	}
	if id == "" {
		return nil, nil, errors.New("empty aria id")
	}
	log, err := r.backend.OpenFigIR(id)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", id, err)
	}
	entries := log.Read()
	msgs := make([]message.Message, 0, len(entries))
	for _, e := range entries {
		m := e.Payload
		// The entry's LT is authoritative; the payload's copy may predate the field.
		m.LogicalTime = e.LT
		msgs = append(msgs, m)
	}
	return msgs, entries, nil
}

func (r *AriaReader) Context(id string) ([]message.Message, *aria.Metrics, error) {
	msgs, _, err := r.messages(id)
	if err != nil {
		return nil, nil, err
	}
	return msgs, r.metrics(id, msgs), nil
}

// Page serves sealed history; at.Turn == 0 with before means the tail, and
// floor applies to a backward read only.
func (r *AriaReader) Page(id string, at, floor aria.Anchor, budget int, before bool) (aria.Page, error) {
	srv, metrics, err := r.serverFor(id)
	if err != nil {
		return aria.Page{}, err
	}
	var page aria.Page
	if before {
		page = srv.ReadBefore(at, floor, budget)
	} else {
		page = srv.Read(at, budget)
	}
	page.Metrics = metrics
	return page, nil
}

// ForkedFrom tells the reader child shares parent's history up to its fork
// base, so its first read can start from parent's turns.
func (r *AriaReader) ForkedFrom(child, parent string) {
	if r == nil || child == "" || parent == "" {
		return
	}
	r.mu.Lock()
	r.forkedFrom[child] = parent
	r.mu.Unlock()
}

// serverFor refreshes when the log has grown past the server's tail: a
// dormant aria still takes hub writes. The tail probe is one ReadPage, not a
// walk, and a refresh recomposes from the newest turn on. A new server starts
// from the turns of the aria it was forked from when one is resident.
func (r *AriaReader) serverFor(id string) (*aria.Server, *aria.Metrics, error) {
	if r == nil || r.backend == nil {
		return nil, nil, errors.New("no backend")
	}
	if id == "" {
		return nil, nil, errors.New("empty aria id")
	}
	r.mu.Lock()
	ra := r.servers[id]
	if ra == nil {
		srv := aria.NewServer()
		srv.BindCache(id, r.uiCache)
		ra = &readAria{srv: srv}
		r.servers[id] = ra
	}
	r.mu.Unlock()

	ra.mu.Lock()
	defer ra.mu.Unlock()
	log, err := r.backend.OpenFigIR(id)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", id, err)
	}
	tailLT := uint64(0)
	if last, _ := log.ReadPage(0, ^uint64(0), 1); len(last) > 0 {
		tailLT = last[0].LT
	}
	if b := ra.srv.TailBracket(); b != 0 && tailLT <= b {
		return ra.srv, ra.metrics, nil
	}
	var from uint64
	ok := false
	if ra.srv.LastTurn() == 0 {
		from, ok = r.adoptFork(id, ra.srv)
	} else {
		from, ok = ra.srv.ReopenTail()
	}
	if ok {
		ra.srv.AppendSealed(r.project(id, log, from))
	} else if err := r.restore(id, ra); err != nil {
		return nil, nil, err
	}
	ra.metrics = r.sidecarMetrics(id, log, tailLT)
	return ra.srv, ra.metrics, nil
}

func (r *AriaReader) restore(id string, ra *readAria) error {
	msgs, entries, err := r.messages(id)
	if err != nil {
		return err
	}
	turns := []aria.Turn(nil)
	if r.proj != nil {
		turns = r.proj.Turns(msgs)
		if fb, ok := r.backend.(formdelta.Backend); ok {
			formdelta.Attach(turns, formdelta.PerRecord(fb, id, entries))
		}
	}
	ra.srv.Restore(turns)
	return nil
}

// SeedAgent fills a waking agent's empty turn server: from this reader's own
// server for id when it holds the whole log, else as a fork's first read
// starts, from the aria it was forked from.
func (r *AriaReader) SeedAgent(id string, srv *aria.Server) (uint64, bool) {
	if r == nil || r.backend == nil || r.proj == nil {
		return 0, false
	}
	log, err := r.backend.OpenFigIR(id)
	if err != nil {
		return 0, false
	}
	tail, ok := log.PeekTail()
	if !ok {
		return 0, false
	}
	r.mu.Lock()
	ra := r.servers[id]
	r.mu.Unlock()
	if ra != nil {
		ra.mu.Lock()
		from, ok := uint64(0), false
		if ra.srv.TailBracket() >= tail.LT {
			from, ok = srv.AdoptBelow(ra.srv, tail.LT)
		}
		ra.mu.Unlock()
		if ok {
			return from, true
		}
	}
	return r.adoptFork(id, srv)
}

// adoptFork seeds an empty server from a resident server of the aria id was
// forked from, or of its storage parent, when that one holds every record
// below the fork base (the base is the first LT the child owns).
func (r *AriaReader) adoptFork(id string, srv *aria.Server) (uint64, bool) {
	cb, ok := r.backend.(store.ChainBackend)
	if !ok || r.proj == nil {
		return 0, false
	}
	links := cb.LineageLinks(id)
	if len(links) < 2 {
		return 0, false
	}
	base := links[len(links)-1].BaseLT
	if base < 2 {
		return 0, false
	}
	inherited := base - 1
	r.mu.Lock()
	candidates := []string{r.forkedFrom[id], links[len(links)-2].Node}
	r.mu.Unlock()
	for _, from := range candidates {
		for _, src := range r.resident(from) {
			if src.TailBracket() < inherited {
				continue
			}
			if lt, ok := srv.AdoptBelow(src, inherited); ok {
				return lt, true
			}
		}
	}
	return 0, false
}

// resident is every materialized turn server of id: a live agent's, then
// this reader's own.
func (r *AriaReader) resident(id string) []*aria.Server {
	if id == "" {
		return nil
	}
	var out []*aria.Server
	if r.liveTurns != nil {
		if srv := r.liveTurns(id); srv != nil {
			out = append(out, srv)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ra := r.servers[id]; ra != nil {
		out = append(out, ra.srv)
	}
	return out
}

// project composes the turns opening at or after from, with the form deltas
// seeded from the record before it.
func (r *AriaReader) project(id string, log arialog.Log[message.Message], from uint64) []aria.Turn {
	entries := log.ReadFrom(from, 0)
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	turns := r.proj.Turns(msgs)
	if fb, ok := r.backend.(formdelta.Backend); ok && len(entries) > 0 {
		seed := formdelta.Seed{}
		if prev, _ := log.ReadPage(0, from, 1); len(prev) > 0 {
			seed = formdelta.SeedFrom(prev[len(prev)-1])
		}
		formdelta.Attach(turns, formdelta.PerRecordFrom(fb, id, seed, entries))
	}
	return turns
}

func (r *AriaReader) FormSince(id string, since uint64) (store.FormSince, error) {
	if r == nil || r.backend == nil {
		return store.FormSince{}, errors.New("no backend")
	}
	out, err := r.backend.FormSince(id, since)
	if err != nil {
		return store.FormSince{}, fmt.Errorf("form %s: %w", id, err)
	}
	return out, nil
}

// Form returns the version too: a mirroring client needs it to order deltas.
func (r *AriaReader) Form(id string) (form.Snapshot, uint64, error) {
	if r == nil || r.backend == nil {
		return form.Snapshot{}, 0, errors.New("no backend")
	}
	snap, err := r.backend.FormState(id)
	if err != nil {
		return form.Snapshot{}, 0, fmt.Errorf("form %s: %w", id, err)
	}
	version, err := r.backend.FormVersion(id)
	if err != nil {
		return form.Snapshot{}, 0, fmt.Errorf("form %s version: %w", id, err)
	}
	return snap, version, nil
}

func (r *AriaReader) sidecarMetrics(id string, log arialog.Log[message.Message], tailLT uint64) *aria.Metrics {
	meta, err := r.backend.Meta(id)
	if err != nil || meta == nil {
		msgs, _, err := r.messages(id)
		if err != nil {
			return &aria.Metrics{}
		}
		return r.metrics(id, msgs)
	}
	held := *meta
	if held.LastFigaroLT > 0 && held.LastFigaroLT < tailLT {
		for _, e := range log.ReadFrom(held.LastFigaroLT+1, int(tailLT-held.LastFigaroLT)) {
			if e.LT > held.LastFigaroLT && e.LT <= tailLT {
				held.Fold(e)
			}
		}
	}
	m := &aria.Metrics{
		ContextTokens: held.ContextTokens, ContextExact: held.ContextExact,
		TokensIn: held.TokensIn, TokensOut: held.TokensOut,
		CacheReadTokens: held.CacheReadTokens, CacheWriteTokens: held.CacheWriteTokens,
	}
	if snap, _, err := r.Form(id); err == nil {
		m.Mantra = snapString(snap, "mantra")
		r.models.Want(models.Subject(snap))
	}
	return m
}

// metrics matches the agent's refreshMetricsFrom minus what only a live turn knows.
func (r *AriaReader) metrics(id string, msgs []message.Message) *aria.Metrics {
	contextTokens, exact := tokens.ContextSize(msgs)
	m := &aria.Metrics{ContextTokens: contextTokens, ContextExact: exact}
	for _, msg := range msgs {
		if u := msg.Usage; u != nil {
			m.TokensIn += u.InputTokens
			m.TokensOut += u.OutputTokens
			m.CacheReadTokens += u.CacheReadTokens
			m.CacheWriteTokens += u.CacheWriteTokens
		}
	}
	if snap, _, err := r.Form(id); err == nil {
		m.Mantra = snapString(snap, "mantra")
		r.models.Want(models.Subject(snap))
	}
	return m
}

func snapString(snap form.Snapshot, key string) string {
	raw, ok := snap.Get(key)
	if !ok {
		return ""
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return v
}
