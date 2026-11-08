package store

import (
	"path/filepath"

	"github.com/jack-work/figaro/internal/store/xwal"
)

type Quarantined = xwal.Quarantined

type QuarantineRelease = xwal.QuarantineRelease

var ErrNotQuarantinedAt = xwal.ErrNotQuarantined

func (b *XwalBackend) QuarantineDir() string {
	return filepath.Join(filepath.Dir(b.root), "quarantine")
}

func (b *XwalBackend) ScanQuarantine() ([]Quarantined, error) {
	return b.store.trunks.ScanQuarantine()
}

func (b *XwalBackend) ReleaseQuarantine(id, channel string, offset int64, apply bool) (QuarantineRelease, error) {
	if apply {
		b.forget(id)
	}
	rel, err := b.store.trunks.ReleaseQuarantine(id, channel, offset, b.QuarantineDir(), apply)
	if apply {
		b.forget(id)
	}
	return rel, err
}

func (b *XwalBackend) forget(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.open, id)
	if f := b.forms[id]; f != nil {
		f.Close()
	}
	delete(b.forms, id)
	delete(b.metas, id)
}
