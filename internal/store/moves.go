package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/xwal"
)

type MoveState string

const (
	MovePending MoveState = "pending"
	MoveFinal   MoveState = "final"
	MoveAborted MoveState = "aborted"
)

type MovedFacts struct {
	MintedBy  string   `json:"minted_by,omitempty"`
	Ancestors []string `json:"ancestors,omitempty"`
}

type Departure struct {
	Token     string                `json:"token"`
	To        string                `json:"to"`
	State     MoveState             `json:"state"`
	AtMS      int64                 `json:"at_ms"`
	Component xwal.Component        `json:"component"`
	Facts     map[string]MovedFacts `json:"facts"`
}

type Arrival struct {
	Token  string   `json:"token"`
	From   string   `json:"from"`
	Caller string   `json:"caller"`
	IDs    []string `json:"ids"`
	Landed bool     `json:"landed,omitempty"`
}

type Tombstone struct {
	ID    string
	To    string
	State MoveState
	Facts MovedFacts
}

type Moves struct {
	dir string

	tombs atomic.Int64

	mu       sync.RWMutex
	departed map[string]*Departure
	byID     map[string]string
	arrived  map[string]*Arrival
}

func OpenMoves(root string) (*Moves, error) {
	m := &Moves{
		dir:      filepath.Join(root, "_moves"),
		departed: map[string]*Departure{},
		byID:     map[string]string{},
		arrived:  map[string]*Arrival{},
	}
	for _, sub := range []string{"out", "in"} {
		if err := os.MkdirAll(filepath.Join(m.dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	outs, err := readAllJSON[Departure](filepath.Join(m.dir, "out"))
	if err != nil {
		return nil, err
	}
	for _, d := range outs {
		m.indexLocked(d)
	}
	ins, err := readAllJSON[Arrival](filepath.Join(m.dir, "in"))
	if err != nil {
		return nil, err
	}
	for _, a := range ins {
		m.arrived[a.Token] = a
	}
	return m, nil
}

func readAllJSON[T any](dir string) ([]*T, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*T
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		v := new(T)
		if err := json.Unmarshal(body, v); err != nil {
			return nil, fmt.Errorf("store: %s: %w", filepath.Join(dir, e.Name()), err)
		}
		out = append(out, v)
	}
	return out, nil
}

func (m *Moves) indexLocked(d *Departure) {
	m.departed[d.Token] = d
	if d.State == MoveAborted {
		return
	}
	for _, id := range d.Component.TrunkIDs() {
		m.byID[id] = d.Token
	}
	m.tombs.Store(int64(len(m.byID)))
}

func (m *Moves) write(sub, token string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Join(m.dir, sub)
	f, err := os.CreateTemp(dir, "."+token+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, token+".json")); err != nil {
		os.Remove(tmp)
		return err
	}
	return disk.SyncDir(dir)
}

func (m *Moves) remove(sub, token string) error {
	dir := filepath.Join(m.dir, sub)
	if err := os.Remove(filepath.Join(dir, token+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return disk.SyncDir(dir)
}

func (m *Moves) Depart(d Departure) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range d.Component.TrunkIDs() {
		if tok, ok := m.byID[id]; ok {
			return fmt.Errorf("store: %s already departed in move %s", id, tok)
		}
	}
	d.State = MovePending
	if err := m.write("out", d.Token, d); err != nil {
		return err
	}
	m.indexLocked(&d)
	return nil
}

func (m *Moves) Settle(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.departed[token]
	if !ok {
		return fmt.Errorf("store: no departure %s", token)
	}
	next := *d
	next.State = MoveFinal
	if err := m.write("out", token, next); err != nil {
		return err
	}
	m.departed[token] = &next
	return nil
}

func (m *Moves) Cancel(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.departed[token]
	if !ok {
		return nil
	}
	if d.State == MoveFinal {
		return fmt.Errorf("store: move %s is final", token)
	}
	if err := m.remove("out", token); err != nil {
		return err
	}
	delete(m.departed, token)
	m.unindexLocked(token, d.Component.TrunkIDs())
	return nil
}

func (m *Moves) unindexLocked(token string, ids []string) {
	for _, id := range ids {
		if m.byID[id] == token {
			delete(m.byID, id)
		}
	}
	m.tombs.Store(int64(len(m.byID)))
}

func (m *Moves) Abort(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.departed[token]
	if !ok {
		return fmt.Errorf("store: no departure %s", token)
	}
	if d.State == MoveFinal {
		return fmt.Errorf("store: move %s is final", token)
	}
	next := *d
	next.State = MoveAborted
	if err := m.write("out", token, next); err != nil {
		return err
	}
	m.departed[token] = &next
	m.unindexLocked(token, next.Component.TrunkIDs())
	return nil
}

func (m *Moves) Departure(token string) (Departure, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.departed[token]
	if !ok {
		return Departure{}, false
	}
	return *d, true
}

func (m *Moves) Any() bool { return m.tombs.Load() > 0 }

func (m *Moves) Tombstone(id string) (Tombstone, bool) {
	if m.tombs.Load() == 0 {
		return Tombstone{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	tok, ok := m.byID[id]
	if !ok {
		return Tombstone{}, false
	}
	d := m.departed[tok]
	return Tombstone{ID: id, To: d.To, State: d.State, Facts: d.Facts[id]}, true
}

func (m *Moves) MatchTombstones(prefix string, limit int) []string {
	if m.tombs.Load() == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for id := range m.byID {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (m *Moves) Departures() []Departure {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Departure, 0, len(m.departed))
	for _, d := range m.departed {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AtMS < out[j].AtMS })
	return out
}

func (m *Moves) Arrive(a Arrival) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.arrived[a.Token]; ok {
		return fmt.Errorf("store: move %s already arriving", a.Token)
	}
	if err := m.write("in", a.Token, a); err != nil {
		return err
	}
	m.arrived[a.Token] = &a
	return nil
}

func (m *Moves) Arrival(token string) (Arrival, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.arrived[token]
	if !ok {
		return Arrival{}, false
	}
	return *a, true
}

func (m *Moves) Land(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.arrived[token]
	if !ok {
		return fmt.Errorf("store: no arrival %s", token)
	}
	next := *a
	next.Landed = true
	if err := m.write("in", token, next); err != nil {
		return err
	}
	m.arrived[token] = &next
	return nil
}

func (m *Moves) Abandon(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.arrived[token]
	if !ok {
		return nil
	}
	if a.Landed {
		return fmt.Errorf("store: move %s has landed", token)
	}
	if err := m.remove("in", token); err != nil {
		return err
	}
	delete(m.arrived, token)
	return nil
}

func (m *Moves) Reclaim(ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	back := map[string]bool{}
	for _, id := range ids {
		back[id] = true
	}
	touched := map[string]bool{}
	for _, id := range ids {
		if tok, ok := m.byID[id]; ok {
			touched[tok] = true
		}
	}
	for tok := range touched {
		d := *m.departed[tok]
		var keep []xwal.ComponentNode
		for _, n := range d.Component.Nodes {
			if !back[n.Trunk] {
				keep = append(keep, n)
			}
		}
		d.Component.Nodes = keep
		facts := map[string]MovedFacts{}
		for id, f := range d.Facts {
			if !back[id] {
				facts[id] = f
			}
		}
		d.Facts = facts
		if len(keep) == 0 {
			if err := m.remove("out", tok); err != nil {
				return err
			}
			delete(m.departed, tok)
		} else {
			if err := m.write("out", tok, d); err != nil {
				return err
			}
			m.departed[tok] = &d
		}
		for id := range back {
			if m.byID[id] == tok {
				delete(m.byID, id)
			}
		}
	}
	m.tombs.Store(int64(len(m.byID)))
	return nil
}
