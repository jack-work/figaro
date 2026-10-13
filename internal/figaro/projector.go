package figaro

import (
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

// Projector is the only way the core reaches the projection; it may be nil.
type Projector interface {
	Turns(msgs []message.Message) []aria.Turn

	// InquirySegments goes through the projector so the engine never imports compose.
	InquirySegments(m message.Message) []aria.InquirySegment

	// Nodes projects the open streaming region. The second return is the count of
	// leading nodes identical to the previous call's; the slice is not mutated afterwards.
	Nodes(msgs []message.Message, tails, argPartials map[string]string) (prefix, suffix []livedoc.Node, stable int)

	ResetTools()
	// ToolOpened is when the model began writing the call; ToolStarted/ToolFinished bracket the run.
	ToolOpened(id string, at int64)
	ToolStarted(id string, at int64)
	ToolFinished(id string, at int64)
}

func (a *Agent) projTurns(msgs []message.Message) []aria.Turn {
	if a.proj == nil {
		return nil
	}
	return a.proj.Turns(msgs)
}

func (a *Agent) projInquirySegments(m message.Message) []aria.InquirySegment {
	if a.proj == nil {
		return nil
	}
	return a.proj.InquirySegments(m)
}

func (a *Agent) projNodes(msgs []message.Message, tails, argPartials map[string]string) (prefix, suffix []livedoc.Node, stable int) {
	if a.proj == nil {
		return nil, nil, 0
	}
	return a.proj.Nodes(msgs, tails, argPartials)
}

// attachFormDeltas folds form-state windows onto sealed turns as AriaReader
// does for a dormant aria, so a transcript reads the same live or not.
func (a *Agent) attachFormDeltas(turns []aria.Turn, entries []store.Entry[message.Message]) []aria.Turn {
	fb, ok := a.backend.(formdelta.Backend)
	if !ok || len(turns) == 0 || len(entries) == 0 {
		return turns
	}
	formdelta.Attach(turns, formdelta.PerRecord(fb, a.id, entries))
	return turns
}

// materializeTurns reads the log once, composes, and attaches form deltas.
func (a *Agent) materializeTurns() []aria.Turn {
	if a.figLog == nil {
		return nil
	}
	entries := a.figLog.Read()
	return a.attachFormDeltas(a.projTurns(unwrapMessages(entries)), entries)
}

// stampSealDeltas writes form state onto the turn about to be sealed; the
// provider stream carries none. The window opens at the previous turn's last
// node-bearing record, not this turn's first: a fork's birth record sits between.
func (a *Agent) stampSealDeltas() {
	fb, ok := a.backend.(formdelta.Backend)
	if !ok || a.figLog == nil || a.ariaSrv == nil {
		return
	}
	a.ariaSrv.StampTail(func(tail *aria.Turn, prev []aria.Turn) {
		deltas, ok := a.windowDeltas(fb, priorBoundary(prev))
		if !ok {
			return
		}
		// The window is the last turn's, so AttachOne needs no seam and no LT range.
		formdelta.AttachOne(tail, deltas)
	})
}

// openingFormDeltas is the state a turn enters with: a fork's birth patch is
// in that window and its banner must be on the turn when the question commits.
func (a *Agent) openingFormDeltas() map[string]livedoc.FormDelta {
	fb, ok := a.backend.(formdelta.Backend)
	if !ok || a.figLog == nil || a.ariaSrv == nil {
		return nil
	}
	deltas, ok := a.windowDeltas(fb, a.ariaSrv.TailNodeLT())
	if !ok {
		return nil
	}
	var opening aria.Turn
	formdelta.AttachOne(&opening, deltas)
	return opening.FormDeltas
}

// windowDeltas derives per-record deltas after the boundary record, seeded from its cursors.
func (a *Agent) windowDeltas(fb formdelta.Backend, from uint64) (map[uint64]map[string]livedoc.FormDelta, bool) {
	entries, _ := a.figLog.ReadPage(from, 0, 0)
	if len(entries) == 0 {
		return nil, false
	}
	seed := formdelta.Seed{}
	if entries[0].LT == from {
		seed = formdelta.SeedFrom(entries[0])
		entries = entries[1:]
	}
	if len(entries) == 0 {
		return nil, false
	}
	return formdelta.PerRecordFrom(fb, a.id, seed, entries), true
}

// priorBoundary is the last record the previous turn drew.
func priorBoundary(prev []aria.Turn) uint64 {
	if len(prev) == 0 {
		return 0
	}
	return aria.PriorBoundaryOf(prev[0])
}
