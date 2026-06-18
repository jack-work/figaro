package figaro

import "github.com/jack-work/figaro/internal/rpc"

// readHardCap bounds the sealed messages returned by one figaro.read,
// matching the angelus aria.read cap.
const readHardCap = 1000

// Read serves a windowed slice of the aria log plus the open tail, the
// figaro-socket generalization of aria.read. With Follow the caller
// keeps receiving live log.* frames on the same connection afterward
// (the connection is already subscribed); this returns the catch-up
// batch. Delta mode for the live tail is negotiated separately.
func (a *Agent) Read(req rpc.ReadRequest) rpc.ReadResponse {
	entries := a.figLog.Read()
	var tail uint64
	if n := len(entries); n > 0 {
		tail = entries[n-1].LT
	}

	from := req.From
	if req.Last > 0 {
		if req.Last >= tail {
			from = 1
		} else {
			from = tail - req.Last + 1
		}
	}
	if from == 0 {
		from = 1
	}

	limit := req.Limit
	if limit == 0 || limit > readHardCap {
		limit = readHardCap
	}

	out := make([]rpc.LogEntry, 0, limit)
	var nextFrom uint64
	for _, e := range entries {
		if e.LT < from {
			continue
		}
		if uint64(len(out)) >= limit {
			nextFrom = e.LT
			break
		}
		m := e.Payload
		m.LogicalTime = e.LT
		out = append(out, rpc.LogEntry{Index: e.LT, Message: m})
	}
	if nextFrom == 0 {
		nextFrom = tail + 1
		if from > nextFrom {
			nextFrom = from // read past the end: resume from the same index
		}
	}

	resp := rpc.ReadResponse{Entries: out, NextFrom: nextFrom, Tail: tail}

	a.mu.RLock()
	if a.openLive {
		resp.Live = true
		if a.openIdx >= from {
			oe := rpc.OpenEntry{
				Index:   a.openIdx,
				Version: a.openVer,
				Open:    true,
				Message: cloneMessage(a.openMsg),
			}
			resp.Open = &oe
		}
	}
	a.mu.RUnlock()
	return resp
}
