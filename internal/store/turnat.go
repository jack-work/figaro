package store

import (
	"github.com/jack-work/figaro/api/message"
)

// TurnAtLT is the turn owning lt: the turn of the record at lt or, past the
// end, of the last record; exact reports which.
//
// The answer is local. The IR door stamps every record with its turn and
// MigrateStampTurnIDs (channel v5) stamped the rest, so the only zero stamps
// are records written around the door: a birth record at a fork base, the
// records before the first question. Those belong to the nearest non-zero
// stamp at or below, so the read is one record in the common case and a short
// backward window otherwise. Positional reads only, never a walk from LT 1.
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
	// One record first, then windows for a run of zero-stamped records.
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

// turnScanPage is small: a record can carry a whole tool output, and the
// stamped record is almost always the next one down.
const turnScanPage = 16
