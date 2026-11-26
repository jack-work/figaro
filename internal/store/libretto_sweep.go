package store

import (
	"log/slog"
)

func (b *XwalBackend) SweepLibrettos() int {
	if b.closed.Load() {
		return 0
	}
	studied, _, unreadable := b.studiersBySource()
	if unreadable > 0 {
		slog.Warn("libretto: sweep skipped, bound forms could not be read", "unreadable", unreadable)
		return 0
	}

	b.mu.Lock()
	generation := b.librettoSweeps
	b.librettoSweeps++
	if b.idleLibrettos == nil {
		b.idleLibrettos = map[string]uint64{}
	}
	present := map[string]bool{}
	for _, st := range b.store.trunks.Stumps() {
		if source, ok := SourceOfLibretto(st.Name); ok {
			present[source] = true
		}
	}
	for source := range b.idleLibrettos {
		if !present[source] {
			delete(b.idleLibrettos, source)
		}
	}
	var due []string
	for source := range present {
		if lib := b.librettos[source]; (lib != nil && lib.Refs() > 0) || len(studied[source]) > 0 {
			delete(b.idleLibrettos, source)
			continue
		}
		since, noted := b.idleLibrettos[source]
		if !noted {
			b.idleLibrettos[source] = generation
			continue
		}
		if since < generation {
			due = append(due, source)
		}
	}
	b.mu.Unlock()

	collected := 0
	for _, source := range due {
		if b.collectLibretto(source) {
			collected++
		}
	}
	return collected
}

func (b *XwalBackend) collectLibretto(source string) bool {
	b.mu.Lock()
	if _, idle := b.idleLibrettos[source]; !idle || b.opening[source] != nil {
		b.mu.Unlock()
		return false
	}
	lib := b.librettos[source]
	if lib != nil && lib.Refs() > 0 {
		b.mu.Unlock()
		return false
	}
	delete(b.idleLibrettos, source)
	delete(b.librettos, source)
	gate := &librettoOpening{done: make(chan struct{})}
	if b.opening == nil {
		b.opening = map[string]*librettoOpening{}
	}
	b.opening[source] = gate
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.opening, source)
		b.mu.Unlock()
		close(gate.done)
	}()
	if lib != nil {
		lib.Close()
	}
	if err := b.store.CollectStump(LibrettoID(source)); err != nil {
		slog.Warn("libretto: could not collect an unstudied copy", "source", source, "err", err)
		return false
	}
	slog.Info("libretto: collected an unstudied copy", "source", source)
	return true
}
