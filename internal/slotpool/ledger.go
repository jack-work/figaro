package slotpool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"
)

type State string

const (
	Minting   State = "minting"
	Live      State = "live"
	Releasing State = "releasing"
	Released  State = "released"
)

var (
	ErrNoSuchLease    = errors.New("no such lease")
	ErrSlotLeased     = errors.New("the slot has a lease that is not released")
	ErrKeyLeased      = errors.New("the key has a lease that is not released")
	ErrGenerationUsed = errors.New("the ledger already names that generation of the slot, or a later one")
	ErrWrongState     = errors.New("the lease is not in a state that allows it")
)

const (
	ledgerVersion = 1
	releasedKept  = 256
)

var keySyntax = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type Lease struct {
	Pool       string   `json:"pool"`
	Slot       string   `json:"slot"`
	Key        string   `json:"key"`
	Generation uint64   `json:"generation"`
	State      State    `json:"state"`
	Holder     string   `json:"holder"`
	Rou2Lease  string   `json:"rou2_lease,omitempty"`
	SayBearer  string   `json:"say_bearer,omitempty"`
	Minted     []string `json:"minted,omitempty"`
	At         int64    `json:"at"`
}

func (l Lease) WithoutBearer() Lease {
	l.SayBearer = ""
	l.Minted = slices.Clone(l.Minted)
	return l
}

type ledgerFile struct {
	Version  int               `json:"version"`
	Leases   []Lease           `json:"leases"`
	Floors   map[string]uint64 `json:"floors"`
	Pages    []Page            `json:"pages"`
	LastPage map[string]uint64 `json:"last_page"`
	Dropped  map[string]uint64 `json:"dropped"`
}

func (f ledgerFile) clone() ledgerFile {
	out := ledgerFile{
		Version:  ledgerVersion,
		Leases:   make([]Lease, len(f.Leases)),
		Floors:   maps.Clone(f.Floors),
		Pages:    slices.Clone(f.Pages),
		LastPage: maps.Clone(f.LastPage),
		Dropped:  maps.Clone(f.Dropped),
	}
	for i, l := range f.Leases {
		l.Minted = slices.Clone(l.Minted)
		out.Leases[i] = l
	}
	if out.Floors == nil {
		out.Floors = map[string]uint64{}
	}
	if out.LastPage == nil {
		out.LastPage = map[string]uint64{}
	}
	if out.Dropped == nil {
		out.Dropped = map[string]uint64{}
	}
	return out
}

type Ledger struct {
	mu      sync.Mutex
	path    string
	found   bool
	now     func() time.Time
	file    ledgerFile
	waiting map[string]chan struct{}
}

func OpenLedger(path string) (*Ledger, error) {
	if err := privateDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if err := removeUnfinishedWrites(path); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	l := &Ledger{path: path, now: time.Now, waiting: map[string]chan struct{}{}}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		l.file = ledgerFile{}.clone()
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("ledger: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var file ledgerFile
	if err := dec.Decode(&file); err != nil || file.Version != ledgerVersion {
		return nil, fmt.Errorf("ledger %s is not one this build wrote (version %d, %v); remove it and the daemon recovers as from a lost ledger, releasing every lease", path, file.Version, err)
	}
	l.file, l.found = file.clone(), true
	return l, nil
}

func (l *Ledger) Found() bool { return l.found }

func (l *Ledger) commit(change func(*ledgerFile) error) error {
	next := l.file.clone()
	if err := change(&next); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(next, "", " ")
	if err != nil {
		return err
	}
	if err := writeDurably(l.path, append(raw, '\n')); err != nil {
		return err
	}
	l.file = next
	return nil
}

func (f *ledgerFile) row(pool, key string, generation uint64) *Lease {
	for i := range f.Leases {
		if r := &f.Leases[i]; r.Pool == pool && r.Key == key && r.Generation == generation {
			return r
		}
	}
	return nil
}

func CheckKey(key string) error {
	if !keySyntax.MatchString(key) {
		return fmt.Errorf("a lease key is 1 to 128 of A-Z a-z 0-9 . _ : - and this one is not (%d bytes)", len(key))
	}
	return nil
}

func (l *Ledger) Begin(pool, slot, key string, generation uint64, holder string) error {
	if pool == "" || slot == "" || holder == "" {
		return errors.New("ledger: a lease names its pool, its slot and who holds it")
	}
	if err := CheckKey(key); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commit(func(f *ledgerFile) error {
		if generation <= f.Floors[slot] {
			return fmt.Errorf("%w: %s at %d, asked %d", ErrGenerationUsed, slot, f.Floors[slot], generation)
		}
		for _, r := range f.Leases {
			if r.State == Released {
				continue
			}
			if r.Slot == slot {
				return fmt.Errorf("%w: %s", ErrSlotLeased, slot)
			}
			if r.Pool == pool && r.Key == key {
				return ErrKeyLeased
			}
		}
		f.Floors[slot] = generation
		f.Leases = append(f.Leases, Lease{Pool: pool, Slot: slot, Key: key, Generation: generation, State: Minting, Holder: holder, At: l.now().Unix()})
		return nil
	})
}

func (l *Ledger) change(pool, key string, generation uint64, from []State, apply func(*Lease)) (Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var after Lease
	err := l.commit(func(f *ledgerFile) error {
		r := f.row(pool, key, generation)
		if r == nil {
			return ErrNoSuchLease
		}
		if !slices.Contains(from, r.State) {
			return fmt.Errorf("%w: it is %s", ErrWrongState, r.State)
		}
		apply(r)
		after = *r
		after.Minted = slices.Clone(r.Minted)
		return nil
	})
	return after, err
}

func (l *Ledger) Opens(pool, key string, generation uint64, rou2Lease string) error {
	_, err := l.change(pool, key, generation, []State{Minting}, func(r *Lease) { r.Rou2Lease = rou2Lease })
	return err
}

func (l *Ledger) Minted(pool, key string, generation uint64, ids ...string) error {
	_, err := l.change(pool, key, generation, []State{Minting, Live}, func(r *Lease) { r.Minted = append(r.Minted, ids...) })
	return err
}

func (l *Ledger) GoLive(pool, key string, generation uint64, sayBearer string) error {
	if sayBearer == "" {
		return errors.New("ledger: a live lease has a say bearer")
	}
	_, err := l.change(pool, key, generation, []State{Minting}, func(r *Lease) {
		r.State, r.SayBearer, r.At = Live, sayBearer, l.now().Unix()
	})
	return err
}

func (l *Ledger) Release(pool, key string, generation uint64) (Lease, error) {
	return l.change(pool, key, generation, []State{Minting, Live, Releasing}, func(r *Lease) {
		if r.State != Releasing {
			r.State, r.At = Releasing, l.now().Unix()
		}
		r.SayBearer = ""
	})
}

func (l *Ledger) Released(pool, key string, generation uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commit(func(f *ledgerFile) error {
		r := f.row(pool, key, generation)
		if r == nil {
			return ErrNoSuchLease
		}
		if r.State != Releasing {
			return fmt.Errorf("%w: it is %s", ErrWrongState, r.State)
		}
		*r = Lease{Pool: r.Pool, Slot: r.Slot, Key: r.Key, Generation: r.Generation, State: Released, Holder: r.Holder, At: l.now().Unix()}
		released := 0
		for _, x := range f.Leases {
			if x.State == Released {
				released++
			}
		}
		f.Leases = slices.DeleteFunc(f.Leases, func(x Lease) bool {
			if x.State == Released && released > releasedKept {
				released--
				return true
			}
			return false
		})
		return nil
	})
}

func (l *Ledger) Held(pool, key string) (Lease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.file.Leases {
		if r.Pool == pool && r.Key == key && r.State != Released {
			r.Minted = slices.Clone(r.Minted)
			return r, true
		}
	}
	return Lease{}, false
}

func (l *Ledger) Find(pool, key string, generation uint64) (Lease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.file.row(pool, key, generation); r != nil {
		out := *r
		out.Minted = slices.Clone(r.Minted)
		return out, true
	}
	return Lease{}, false
}

func (l *Ledger) Leases() []Lease {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.clone().Leases
}

func (l *Ledger) Floor(slot string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Floors[slot]
}
