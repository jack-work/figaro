package figaro

import (
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// THE JOURNAL IS THE ONE DOOR TO THE CONVERSATION'S DURABLE RECORD, and its
// contract is a single sentence:
//
//	DURABILITY PRECEDES VISIBILITY, AND VISIBILITY IS NOT OPTIONAL.
//
// Appending is not "write, and then remember to tell someone". Telling is part
// of the write.
//
// WHY THIS TYPE EXISTS. Before it, the agent had TWO append sites and FIFTEEN
// places that broadcast, so the correlation between "a record appeared" and
// "somebody was told" was maintained by hand at fifteen points. It was
// forgotten at the one that mattered most often: appendUserPrompt deliberately
// builds no node for a STEER (the projection owns that), and nothing emitted
// afterwards -- so a queued message became durable, its queue row cleared, and
// the text did not appear until the next provider round's first streamed
// chunk. A reader saw the message vanish from the queue and arrive seconds
// later in the transcript.
//
// TWO KINDS OF VISIBILITY, AND CONFLATING THEM IS THE ROOT CAUSE:
//
//   - STRUCTURAL: a durable record appeared. Immediate, non-negotiable.
//   - STREAMING:  an in-flight unit grew. Throttled, coalesced, best-effort.
//
// The steer is structural and was left to the streaming path to pick up, so it
// inherited the wrong clock -- a provider's time-to-first-token. The journal
// carries the structural clock and nothing else; the streaming throttle must
// never be routed through it, or nothing has been fixed.
//
// IT COSTS NOTHING THAT IS NOT ALREADY COMPUTED. Composing is a pure function
// of the turn's window, the record is in that window the instant it appends,
// and streaming recomposes far more often on chunks. This sends a frame we
// already know how to build, at the moment we already know it changed.
type journal struct {
	log store.Log[message.Message]
	// publish announces one durable record. It runs on the caller's goroutine,
	// which is the drain loop, which is where every emit already runs.
	publish func()
}

// Append writes one message and announces it. The error is the WRITE's: a
// failure to publish is logged and swallowed, because the durable truth is not
// conditional on a client having heard it.
func (j *journal) Append(e store.Entry[message.Message]) (store.Entry[message.Message], error) {
	out, err := j.log.Append(e)
	if err != nil {
		// Nothing became true, so nothing is announced.
		return out, err
	}
	if j.publish != nil {
		j.publish()
	}
	return out, nil
}

// Read exposes the log for readers. It is deliberately NOT the whole Log
// interface: see appendable.
func (j *journal) Read() []store.Entry[message.Message] { return j.log.Read() }

// appendable is the compile-time half of the argument, and it is the half that
// matters.
//
// A helper method is a CONVENTION, and conventions are exactly what failed at
// fifteen sites. So the agent must not hold anything it can append to except
// this journal: `a.figLog` keeps its reads, the journal owns the writes, and
// "append without publishing" stops being a discipline anyone remembers and
// becomes a thing that DOES NOT COMPILE.
//
// This var is the assertion that the journal really is the writer; if the log
// ever gains a second write path, this is where it will be noticed.
var _ interface {
	Append(store.Entry[message.Message]) (store.Entry[message.Message], error)
} = (*journal)(nil)

// writer is THE appender, bound on demand.
//
// On demand because an Agent built by hand -- tests do, and so does anything
// that assembles one field at a time -- would otherwise carry a nil journal,
// and the first append would panic or, far worse, tempt someone to reinstate
// a fallback to figLog.Append. A fallback is a second write path, and a second
// write path is the whole defect this type removes.
func (a *Agent) writer() *journal {
	if a.journal == nil {
		a.journal = a.newJournal(a.figLog)
	}
	return a.journal
}

func (a *Agent) newJournal(log store.Log[message.Message]) *journal {
	return &journal{
		log: log,
		publish: func() {
			// The structural emit. inflight is nil by construction here: a
			// durable record has landed, so the in-flight assembly (if any)
			// must not be composed beside it -- that is the duplicated-message
			// frame composeTurn's own comments describe.
			// EVERYTHING COMPOSE TOUCHES MUST BE UP. The journal fires on the
			// FIRST append of a turn, which is earlier than any previous emit
			// site ran -- earlier than the governor exists, in particular, and
			// composeTurn reads gov.Tails(). A publish before the turn's
			// machinery is assembled is not "an early frame", it is a nil
			// dereference on the drain loop.
			if a.proj == nil || a.ariaSrv == nil || a.gov == nil {
				return
			}
			// NO recover() HERE. An earlier version had one, and it turned a
			// real crash into a logged warning -- defeating actProtected,
			// which is the drain loop's own panic handler and the thing that
			// restarts the agent and marks the turn "crashed and was
			// restarted". A defensive recover that hides the failures the
			// system is built to handle is worse than no recover at all.
			a.emitDelta(a.composeTurn(nil))
		},
	}
}
