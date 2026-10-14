package angelus

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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
	mu      sync.Mutex
	servers map[string]*readAria
}

// readAria caches metrics from its last refresh; the log growing is what triggers both.
type readAria struct {
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
	return &AriaReader{backend: backend, proj: proj, uiCache: uiCache, servers: map[string]*readAria{}}
}

// messages also returns the entries: they carry the cursor stamps the
// payload does not, and form-delta assembly reads them.
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
	ra, err := r.serverFor(id)
	if err != nil {
		return aria.Page{}, err
	}
	var page aria.Page
	if before {
		page = ra.srv.ReadBefore(at, floor, budget)
	} else {
		page = ra.srv.Read(at, budget)
	}
	page.Metrics = ra.metrics
	return page, nil
}

// serverFor refreshes when the log has grown past the server's tail: a
// dormant aria still takes hub writes. The tail probe is one ReadPage, not a walk.
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
