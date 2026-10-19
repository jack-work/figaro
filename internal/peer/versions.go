package peer

import (
	"log/slog"
	"maps"
	"sync"
	"time"
)

type Sighting struct {
	Version uint64
	Policy  uint64
	At      time.Time
}

type Versions struct {
	mu    sync.Mutex
	seen  map[string]Sighting
	drift func(node string)
}

func (v *Versions) OnDrift(fn func(node string)) {
	v.mu.Lock()
	v.drift = fn
	v.mu.Unlock()
}

func (v *Versions) Saw(node string, theirs, ours, policy uint64) {
	if v == nil {
		return
	}
	v.mu.Lock()
	prev, had := v.seen[node]
	if v.seen == nil {
		v.seen = map[string]Sighting{}
	}
	v.seen[node] = Sighting{Version: theirs, Policy: policy, At: time.Now()}
	drift := v.drift
	v.mu.Unlock()
	switch {
	case theirs == ours:
	case drift != nil:
		drift(node)
	case !had || prev.Version != theirs:
		slog.Warn("troupe membership version differs", "node", node, "theirs", theirs, "ours", ours)
	}
}

func (v *Versions) Seen(node string) (Sighting, bool) {
	if v == nil {
		return Sighting{}, false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.seen[node]
	return s, ok
}

func (v *Versions) All() map[string]Sighting {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return maps.Clone(v.seen)
}
