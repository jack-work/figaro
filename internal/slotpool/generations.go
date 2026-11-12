package slotpool

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Generations struct {
	mu  sync.Mutex
	dir string
}

func OpenGenerations(dir string) (*Generations, error) {
	if err := privateDir(dir); err != nil {
		return nil, fmt.Errorf("generation counters: %w", err)
	}
	return &Generations{dir: dir}, nil
}

func (g *Generations) path(slot string) string { return filepath.Join(g.dir, slot+".gen") }

func (g *Generations) Last(slot string) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last(slot)
}

func (g *Generations) last(slot string) (uint64, error) {
	raw, err := os.ReadFile(g.path(slot))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("generation counter of %s: %w", slot, err)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("generation counter %s does not hold a number; remove it and the floor comes from the ledger, rou2 and the store", g.path(slot))
	}
	return n, nil
}

func (g *Generations) Next(slot string, floor uint64) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	last, err := g.last(slot)
	if err != nil {
		return 0, err
	}
	highest := max(last, floor)
	if highest == math.MaxUint64 {
		return 0, fmt.Errorf("generation counter of %s is spent", slot)
	}
	if err := removeUnfinishedWrites(g.path(slot)); err != nil {
		return 0, fmt.Errorf("generation counter of %s: %w", slot, err)
	}
	if err := writeDurably(g.path(slot), []byte(strconv.FormatUint(highest+1, 10)+"\n")); err != nil {
		return 0, err
	}
	return highest + 1, nil
}
