package workerpool

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

type PageKind string

const (
	SuccessionFailed PageKind = "succession_failed"
	SuccessionBound  PageKind = "succession_bound"
	PurgeFailed      PageKind = "purge_failed"
	LeaseExhausted   PageKind = "lease_exhausted"
	LeaseClosed      PageKind = "lease_closed"
)

var pageKinds = []PageKind{SuccessionFailed, SuccessionBound, PurgeFailed, LeaseExhausted, LeaseClosed}

const (
	MaxPageWait = 50 * time.Second
	pagesHeld   = 1024
)

var (
	ErrPageWait  = fmt.Errorf("pages waits from 0 to %d seconds", int(MaxPageWait/time.Second))
	ErrPagesFull = fmt.Errorf("the pool already holds %d pages nobody has acknowledged", pagesHeld)
)

var figaroID = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

type Page struct {
	ID         uint64   `json:"id"`
	At         int64    `json:"at"`
	Pool       string   `json:"pool"`
	Key        string   `json:"key"`
	Generation uint64   `json:"generation"`
	Kind       PageKind `json:"kind"`
	Figaro     string   `json:"figaro,omitempty"`
	Dropped    uint64   `json:"dropped,omitempty"`
}

func (l *Ledger) Page(pool, key string, generation uint64, kind PageKind, figaro string) (uint64, error) {
	switch {
	case pool == "":
		return 0, errors.New("ledger: a page names its pool")
	case !slices.Contains(pageKinds, kind):
		return 0, errors.New("ledger: a page's kind is one of the fixed list, and this one is not")
	case !keySyntax.MatchString(key):
		return 0, errors.New("ledger: a page names its lease by a key of the lease syntax")
	case figaro != "" && !figaroID.MatchString(figaro):
		return 0, errors.New("ledger: a page names a figaro by its id")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var id uint64
	full := false
	err := l.commit(func(f *ledgerFile) error {
		held := 0
		for _, p := range f.Pages {
			if p.Pool == pool {
				held++
			}
		}
		if held >= pagesHeld {
			f.Dropped[pool]++
			full = true
			return nil
		}
		id = f.LastPage[pool] + 1
		f.LastPage[pool] = id
		f.Pages = append(f.Pages, Page{ID: id, At: l.now().Unix(), Pool: pool, Key: key, Generation: generation, Kind: kind, Figaro: figaro, Dropped: f.Dropped[pool]})
		delete(f.Dropped, pool)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if full {
		return 0, ErrPagesFull
	}
	l.wake(pool)
	return id, nil
}

func (l *Ledger) Dropped(pool string) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Dropped[pool]
}

func (l *Ledger) wake(pool string) {
	if w, ok := l.waiting[pool]; ok {
		close(w)
		delete(l.waiting, pool)
	}
}

func (l *Ledger) held(pool string) []Page {
	var out []Page
	for _, p := range l.file.Pages {
		if p.Pool == pool {
			out = append(out, p)
		}
	}
	return out
}

func (l *Ledger) Pages(ctx context.Context, pool string, seen []uint64, wait time.Duration) ([]Page, error) {
	if wait < 0 || wait > MaxPageWait {
		return nil, ErrPageWait
	}
	l.mu.Lock()
	acked := slices.ContainsFunc(l.file.Pages, func(p Page) bool { return p.Pool == pool && slices.Contains(seen, p.ID) })
	if acked {
		err := l.commit(func(f *ledgerFile) error {
			f.Pages = slices.DeleteFunc(f.Pages, func(p Page) bool { return p.Pool == pool && slices.Contains(seen, p.ID) })
			return nil
		})
		if err != nil {
			l.mu.Unlock()
			return nil, err
		}
	}
	held := l.held(pool)
	if len(held) > 0 || wait == 0 {
		l.mu.Unlock()
		return held, nil
	}
	l.wake(pool)
	mine := make(chan struct{})
	l.waiting[pool] = mine
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	var err error
	select {
	case <-mine:
	case <-timer.C:
	case <-ctx.Done():
		err = ctx.Err()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.waiting[pool] == mine {
		delete(l.waiting, pool)
	}
	if err != nil {
		return nil, err
	}
	return l.held(pool), nil
}
