package form

import (
	"encoding/json"
	"fmt"
)

const ReplayKey = "system.replay"

type Replay struct {
	Of   string `json:"of"`
	Turn uint64 `json:"turn"`
	LT   uint64 `json:"lt,omitempty"`
}

func (r Replay) String() string { return fmt.Sprintf("%s:%d", r.Of, r.Turn) }

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

type ReplayMasks struct {
	Notice   bool
	Clock    bool
	Identity bool
	Tools    bool
}

func ReadReplayMasks(s Snapshot) ReplayMasks {
	on := func(name string) bool {
		raw, ok := s.Get(ReplayKey + "." + name)
		if !ok {
			return true
		}
		var b bool
		return json.Unmarshal(raw, &b) != nil || b
	}
	tools := on("tools")
	return ReplayMasks{Notice: on("notice"), Clock: on("clock"), Identity: tools && on("identity"), Tools: tools}
}

func (r Replay) Patch(base Snapshot) Patch {
	b, _ := json.Marshal(r)
	return Build(base, map[string]json.RawMessage{ReplayKey: b}, nil)
}
