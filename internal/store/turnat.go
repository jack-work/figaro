package store

import (
	"github.com/jack-work/figaro/api/message"
)

// TurnAtLT is the turn that owns lt in log: the turn of the record AT lt or,
// when the log has no record there (lt is past its end), the turn of its last
// record. exact reports which of the two it was.
//
// THE ANSWER IS LOCAL, and it is local because of an invariant, not a hope.
// The IR door stamps every record it writes with its turn (turnIDFor), and
// MigrateStampTurnIDs (fig IR channel v5) stamped every record written before
// it, so every question in a store that opens carries its own turn id. The
// only records that say 0 are the ones written around the door: a birth record
// at a fork base, the records before an aria's first question. Under
// turns.StampIDs those belong to the turn of the record before them (or to no
// turn, before the first question), so the owner of lt is the nearest
// NON-ZERO stamp at or below it. That is one record in the common case and a
// short backward read across a birth record in the worst.
//
// It used to be a walk from LT 1 whenever the base was past the end of the
// parent's log, which is every head fork, on every hop between two arias.
//
// ONLY INDEXED READS. PeekTail and ReadFrom address the channel by position;
// ReadPage, on a raw node log, materializes the whole log and slices it, so
// the backward read here is built from ReadFrom windows and never from it.
//
// Were the invariant ever broken (an unstamped question below lt), the answer
// would be the stamp BELOW that question: a smaller turn than the truth. For
// the one caller that matters, the fork base, smaller means keeping fewer
// turns across a hop, which costs a re-read and is never wrong.
func TurnAtLT(log Log[message.Message], lt uint64) (turn uint64, exact bool) {
	if log == nil || lt == 0 {
		return 0, false
	}
	tail, ok := log.PeekTail()
	if !ok {
		return 0, false
	}
	top := lt // the highest LT that can answer
	if tail.LT <= lt {
		if tail.Payload.TurnID != 0 {
			return tail.Payload.TurnID, tail.LT == lt
		}
		exact, top = tail.LT == lt, tail.LT
	}
	first := tail.LT > lt
	// One record first: it is the answer almost every time. Then windows, for
	// a run of zero-stamped records between lt and the turn they belong to.
	for size := uint64(1); ; size = turnScanPage {
		start := uint64(1)
		if top > size {
			start = top - size + 1
		}
		window := log.ReadFrom(start, int(top-start+1))
		for i := len(window) - 1; i >= 0; i-- {
			e := window[i]
			if e.LT > top {
				continue
			}
			if first {
				exact, first = e.LT == lt, false
			}
			if id := e.Payload.TurnID; id != 0 {
				return id, exact
			}
		}
		if start <= 1 {
			return 0, exact
		}
		top = start - 1
	}
}

// turnScanPage is how many records one step of the backward read takes once
// the first record was not enough. Small: a record can carry a whole tool
// output, and the stamped record is almost always the next one down.
const turnScanPage = 16
