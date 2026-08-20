package angelus

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

// AriaReader answers the read half of the aria contract: history, context
// and form: straight from the store, with no agent.
type AriaReader struct {
	backend store.Backend
	proj    Projector
	uiCache *aria.ComposedCache

	// servers is the reader's half of the ONE windowed component: an
	// aria.Server per read aria, no open region, its sealed section the
	// same bounded TurnCache the agents use, against the same budget.
	// The shells are tiny once their turns hollow; what this buys is the
	// end of the per-call whole-log recompose (13.1 MB/op thrown away at
	// 10k messages) AND one read path instead of two.
	mu      sync.Mutex
	servers map[string]*readAria
}

// readAria is one read aria's windowed server plus the metrics computed
// at its last refresh. Metrics only change when the log grows, and log
// growth is exactly what triggers a refresh, so serving them cached is
// not staleness.
type readAria struct {
	srv     *aria.Server
	metrics *aria.Metrics
}

// Projector converts fig IR into UI IR. Named here rather than imported
// from internal/figaro so the reader does not depend on the agent package:
// the whole point is that this path has no agent in it.
type Projector interface {
	Turns(msgs []message.Message) []aria.Turn
}

func NewAriaReader(backend store.Backend, proj Projector) *AriaReader {
	return NewAriaReaderBounded(backend, proj, nil)
}

// NewAriaReaderBounded is NewAriaReader against the shared composed cache.
func NewAriaReaderBounded(backend store.Backend, proj Projector, uiCache *aria.ComposedCache) *AriaReader {
	return &AriaReader{backend: backend, proj: proj, uiCache: uiCache, servers: map[string]*readAria{}}
}

// messages decodes the aria's IR. The backend hands back the same shared
// instance a live agent would hold, so this is lock-free against writes.
// The ENTRIES come back too: they carry the cursor stamps
// (FormChannelVersion, StudyVersions) that the message payload does not,
// and the form-delta assembly reads them.
func (r *AriaReader) messages(id string) ([]message.Message, []store.Entry[message.Message], error) {
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
		// The entry's LT is authoritative; the payload's copy may predate
		// the field. The projection does exactly this.
		m.LogicalTime = e.LT
		msgs = append(msgs, m)
	}
	return msgs, entries, nil
}

// Context returns the aria's fig IR plus the metrics a client renders
// beside it.
func (r *AriaReader) Context(id string) ([]message.Message, *aria.Metrics, error) {
	msgs, _, err := r.messages(id)
	if err != nil {
		return nil, nil, err
	}
	return msgs, r.metrics(id, msgs), nil
}

// Page serves one window of sealed history from the shared windowed
// component. at.Turn == 0 with before means the tail. There is no open
// streaming region: a dormant aria has no turn in flight, which is what
// makes it dormant.
func (r *AriaReader) Page(id string, at aria.Anchor, budget int, before bool) (aria.Page, error) {
	ra, err := r.serverFor(id)
	if err != nil {
		return aria.Page{}, err
	}
	var page aria.Page
	if before {
		page = ra.srv.ReadBefore(at, budget)
	} else {
		page = ra.srv.Read(at, budget)
	}
	page.Metrics = ra.metrics
	return page, nil
}

// serverFor returns the aria's windowed server, materializing on first
// sight and REFRESHING when the log has grown past the server's tail --
// a dormant aria still takes hub writes (sets, study marks), and serving
// a page that predates them would be the stale-read bug wearing new
// clothes. The tail probe costs one ReadPage(…,1), not a walk.
func (r *AriaReader) serverFor(id string) (*readAria, error) {
	if r == nil || r.backend == nil {
		return nil, errors.New("no backend")
	}
	if id == "" {
		return nil, errors.New("empty aria id")
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

	log, err := r.backend.OpenFigIR(id)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", id, err)
	}
	tailLT := uint64(0)
	if last, _ := log.ReadPage(0, ^uint64(0), 1); len(last) > 0 {
		tailLT = last[0].LT
	}
	if b := ra.srv.TailBracket(); b != 0 && tailLT <= b {
		return ra, nil
	}
	msgs, entries, err := r.messages(id)
	if err != nil {
		return nil, err
	}
	turns := []aria.Turn(nil)
	if r.proj != nil {
		turns = r.proj.Turns(msgs)
		if fb, ok := r.backend.(formdelta.Backend); ok {
			formdelta.Attach(turns, formdelta.PerRecord(fb, id, entries))
		}
	}
	ra.srv.Restore(turns)
	ra.metrics = r.metrics(id, msgs)
	return ra, nil
}

// Form reads the reducible form channel, which is the durable truth, AND the
// version it stands at. The version is not decoration: a client mirroring the
// form applies deltas on top of this snapshot, and without the version it cannot
// tell whether the next delta follows it or whether it missed one.
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

// metrics recomputes what the agent's refreshMetricsFrom computes, minus
// the fields that only a live turn can know. Token counts and context size
// are pure functions of the IR, so they round-trip exactly; a dormant aria
// simply has no provider bound, so the context LIMIT is whatever the
// form last recorded rather than a live lookup.
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
