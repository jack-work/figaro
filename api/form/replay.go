package form

import "encoding/json"

const ReplayKey = "system.replay"

type Replay struct {
	Of   string `json:"of"`
	Turn uint64 `json:"turn"`
	LT   uint64 `json:"lt"`
}

func ReadReplay(s Snapshot) (Replay, bool) {
	raw, ok := s.Get(ReplayKey)
	if !ok {
		return Replay{}, false
	}
	var r Replay
	if err := json.Unmarshal(raw, &r); err != nil || r.Of == "" || r.Turn == 0 {
		return Replay{}, false
	}
	return r, true
}

func (r Replay) Patch() Patch {
	b, err := json.Marshal(r)
	if err != nil {
		return Patch{}
	}
	return Build(Snapshot{}, map[string]json.RawMessage{ReplayKey: b}, nil)
}
