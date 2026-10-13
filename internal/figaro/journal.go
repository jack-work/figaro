package figaro

import (
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// journal is the one write path to the conversation's record: durability
// precedes visibility, and visibility is not optional. A durable record is a
// structural change and emits immediately; the streaming throttle must never
// route through here.
type journal struct {
	log store.Log[message.Message]
	// publish runs on the caller's goroutine, the drain loop.
	publish func()
}

// Append writes and announces. A failed publish is swallowed: the durable
// truth is not conditional on a client having heard it.
func (j *journal) Append(e store.Entry[message.Message]) (store.Entry[message.Message], error) {
	out, err := j.log.Append(e)
	if err != nil {
		return out, err
	}
	if j.publish != nil {
		j.publish()
	}
	return out, nil
}

// Read is deliberately not the whole Log interface; see appendable.
func (j *journal) Read() []store.Entry[message.Message] { return j.log.Read() }

// The agent holds nothing appendable but the journal, so an unpublished
// append does not compile.
var _ interface {
	Append(store.Entry[message.Message]) (store.Entry[message.Message], error)
} = (*journal)(nil)

// writer binds on demand so a hand-built Agent carries no nil journal.
// There is no fallback to figLog.Append: that would be a second write path.
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
			// inflight is nil here: a durable record landed and composing the in-flight
			// assembly beside it duplicates the message. The journal fires on a turn's
			// first append, before gov exists, so bail until the turn's machinery is up.
			if a.proj == nil || a.ariaSrv == nil || a.gov == nil {
				return
			}
			// No recover() here: it would hide the crash from actProtected, the drain loop's panic handler.
			a.emitDelta(a.composeTurn(nil))
		},
	}
}
