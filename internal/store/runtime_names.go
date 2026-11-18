package store

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const runtimeNamesGeneration = 3

type RuntimeNamesReport struct {
	Forms   int   `json:"forms"`
	Renamed int   `json:"renamed"`
	Millis  int64 `json:"ms"`
	Revert  bool  `json:"revert,omitempty"`
}

func (b *XwalBackend) migrateRuntimeNamesAtOpen() error {
	f, err := readSchema(b.root)
	if err != nil {
		return err
	}
	if f.StoreVersion >= runtimeNamesGeneration {
		return nil
	}
	rep, err := b.renameRuntimeKeys(form.MigrateRuntimeNames)
	if err != nil {
		return fmt.Errorf("store %s: runtime key migration: %w", b.root, err)
	}
	if err := stampGeneration(b.root, runtimeNamesGeneration); err != nil {
		return err
	}
	b.runtimeNames = rep
	slog.Info("store generation migrated", "root", b.root, "to", runtimeNamesGeneration,
		"forms", rep.Forms, "renamed", rep.Renamed, "ms", rep.Millis)
	return nil
}

func (b *XwalBackend) RuntimeNamesAtOpen() RuntimeNamesReport { return b.runtimeNames }

func (b *XwalBackend) RevertRuntimeNames() (RuntimeNamesReport, error) {
	rep, err := b.renameRuntimeKeys(form.RevertRuntimeNames)
	rep.Revert = true
	if err != nil {
		return rep, err
	}
	return rep, stampGeneration(b.root, runtimeNamesGeneration-1)
}

const renameWorkers = 128

func (b *XwalBackend) renameRuntimeKeys(rename func(form.Snapshot) form.Patch) (RuntimeNamesReport, error) {
	start := time.Now()
	var ids []string
	for _, n := range b.store.Nodes() {
		if n.Kind == KindConversation || n.Kind == KindForm {
			ids = append(ids, n.ID)
		}
	}
	var (
		renamed atomic.Int64
		next    atomic.Int64
		errMu   sync.Mutex
		first   error
		wg      sync.WaitGroup
	)
	for range min(renameWorkers, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(ids) {
					return
				}
				did, err := b.renameOne(ids[i], rename)
				if err != nil {
					errMu.Lock()
					if first == nil {
						first = fmt.Errorf("%s: %w", ids[i], err)
					}
					errMu.Unlock()
					return
				}
				if did {
					renamed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	rep := RuntimeNamesReport{Forms: len(ids), Renamed: int(renamed.Load()), Millis: time.Since(start).Milliseconds()}
	return rep, first
}

func (b *XwalBackend) renameOne(id string, rename func(form.Snapshot) form.Patch) (bool, error) {
	b.mu.Lock()
	resident := b.forms[id] != nil
	b.mu.Unlock()
	f, err := b.form(id)
	if err != nil {
		return false, err
	}
	if !resident {
		defer b.dropForm(id)
	}
	snap, _ := f.Snapshot()
	p := rename(snap)
	if p.IsIdentity() {
		return false, nil
	}
	_, _, err = f.applyRename(p)
	return err == nil, err
}

func stampGeneration(root string, gen int) error {
	f, err := readSchema(root)
	if err != nil {
		return err
	}
	f.StoreVersion = gen
	return writeSchema(root, f)
}
