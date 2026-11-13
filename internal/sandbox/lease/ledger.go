package lease

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/durable"
)

type State string

const (
	Leasing   State = "leasing"
	Leased    State = "leased"
	Releasing State = "releasing"
	Cordoned  State = "cordoned"
)

const ledgerVersion = 1

type Row struct {
	Node       string    `json:"node"`
	Class      string    `json:"class"`
	Generation uint64    `json:"generation"`
	Sandbox    string    `json:"sandbox,omitempty"`
	Exec       string    `json:"exec,omitempty"`
	State      State     `json:"state"`
	Leaser     string    `json:"leaser"`
	Holders    []string  `json:"holders,omitempty"`
	Nonce      string    `json:"nonce,omitempty"`
	Since      time.Time `json:"since"`
}

func (r Row) Holds(figaro string) bool { return slices.Contains(r.Holders, figaro) }

type file struct {
	Version int   `json:"version"`
	Rows    []Row `json:"rows"`
}

type Ledger struct {
	mu    sync.Mutex
	path  string
	found bool
	now   func() time.Time
	rows  []Row
}

func Open(path string) (*Ledger, error) {
	if err := durable.PrivateDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("sandbox lease ledger: %w", err)
	}
	if err := durable.RemoveUnfinished(path); err != nil {
		return nil, fmt.Errorf("sandbox lease ledger: %w", err)
	}
	l := &Ledger{path: path, now: time.Now}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("sandbox lease ledger: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil || f.Version != ledgerVersion {
		return nil, fmt.Errorf("sandbox lease ledger %s is not one this build wrote (version %d, %v); remove it and every sandbox of every node is reset before it is offered again", path, f.Version, err)
	}
	l.rows, l.found = f.Rows, true
	return l, nil
}

func (l *Ledger) Found() bool { return l.found }

func clone(rows []Row) []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		r.Holders = slices.Clone(r.Holders)
		out[i] = r
	}
	return out
}

func (l *Ledger) commit(change func([]Row) ([]Row, error)) error {
	next, err := change(clone(l.rows))
	if err != nil {
		return err
	}
	sort.Slice(next, func(i, j int) bool {
		if next[i].Class != next[j].Class {
			return next[i].Class < next[j].Class
		}
		return next[i].Generation < next[j].Generation
	})
	raw, err := json.MarshalIndent(file{Version: ledgerVersion, Rows: next}, "", " ")
	if err != nil {
		return err
	}
	if err := durable.Write(l.path, append(raw, '\n')); err != nil {
		return err
	}
	l.rows = next
	return nil
}

func find(rows []Row, class string, generation uint64) int {
	for i, r := range rows {
		if r.Class == class && r.Generation == generation {
			return i
		}
	}
	return -1
}

func findSandbox(rows []Row, sandbox string) int {
	for i, r := range rows {
		if r.Sandbox == sandbox && sandbox != "" {
			return i
		}
	}
	return -1
}

func (l *Ledger) Begin(node, class string, generation uint64, leaser string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commit(func(rows []Row) ([]Row, error) {
		if find(rows, class, generation) >= 0 {
			return nil, fmt.Errorf("sandbox lease %s/%d is already in the ledger", class, generation)
		}
		return append(rows, Row{Node: node, Class: class, Generation: generation, State: Leasing, Leaser: leaser, Since: l.now().UTC()}), nil
	})
}

func (l *Ledger) change(class string, generation uint64, from []State, apply func(*Row) error) (Row, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out Row
	err := l.commit(func(rows []Row) ([]Row, error) {
		i := find(rows, class, generation)
		if i < 0 {
			return nil, fmt.Errorf("sandbox lease %s/%d is not in the ledger", class, generation)
		}
		if !slices.Contains(from, rows[i].State) {
			return nil, fmt.Errorf("sandbox lease %s/%d is %s, not %v", class, generation, rows[i].State, from)
		}
		if err := apply(&rows[i]); err != nil {
			return nil, err
		}
		rows[i].Since = l.now().UTC()
		out = rows[i]
		return rows, nil
	})
	return out, err
}

func (l *Ledger) Leased(class string, generation uint64) (Row, error) {
	return l.change(class, generation, []State{Leasing}, func(r *Row) error {
		if r.Sandbox == "" {
			return fmt.Errorf("sandbox lease %s/%d has no sandbox picked yet", class, generation)
		}
		r.State, r.Holders = Leased, []string{r.Leaser}
		return nil
	})
}

func (l *Ledger) Picked(class string, generation uint64, sandbox, exec string) (Row, error) {
	return l.change(class, generation, []State{Leasing}, func(r *Row) error {
		r.Sandbox, r.Exec = sandbox, exec
		return nil
	})
}

func (l *Ledger) AddHolder(class string, generation uint64, figaro string) (Row, error) {
	return l.change(class, generation, []State{Leased}, func(r *Row) error {
		if !r.Holds(figaro) {
			r.Holders = append(r.Holders, figaro)
		}
		return nil
	})
}

func (l *Ledger) DropHolder(class string, generation uint64, figaro string) (Row, error) {
	return l.change(class, generation, []State{Leased}, func(r *Row) error {
		r.Holders = slices.DeleteFunc(r.Holders, func(h string) bool { return h == figaro })
		return nil
	})
}

func (l *Ledger) Releasing(class string, generation uint64, nonce string) (Row, error) {
	return l.change(class, generation, []State{Leasing, Leased, Releasing, Cordoned}, func(r *Row) error {
		r.State, r.Nonce, r.Holders = Releasing, nonce, nil
		return nil
	})
}

func (l *Ledger) Cordon(class string, generation uint64) (Row, error) {
	return l.change(class, generation, []State{Leasing, Leased, Releasing, Cordoned}, func(r *Row) error {
		r.State, r.Holders = Cordoned, nil
		return nil
	})
}

func (l *Ledger) Close(class string, generation uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commit(func(rows []Row) ([]Row, error) {
		i := find(rows, class, generation)
		if i < 0 {
			return rows, nil
		}
		return slices.Delete(rows, i, i+1), nil
	})
}

func (l *Ledger) Rows() []Row {
	l.mu.Lock()
	defer l.mu.Unlock()
	return clone(l.rows)
}

func (l *Ledger) Of(sandbox string) (Row, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i := findSandbox(l.rows, sandbox); i >= 0 {
		return clone(l.rows[i : i+1])[0], true
	}
	return Row{}, false
}

func (l *Ledger) Floor(class string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var floor uint64
	for _, r := range l.rows {
		if r.Class == class {
			floor = max(floor, r.Generation)
		}
	}
	return floor
}
