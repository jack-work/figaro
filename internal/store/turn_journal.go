package store

import (
	"fmt"
)

// TurnJournal stores opaque, versioned payloads owned by internal/figaro.
type TurnJournal interface {
	Checkpoint(targetMainLT uint64, payload []byte) error
	Sync() error
	// Latest returns the newest record at or beyond targetMainLT.
	Latest(targetMainLT uint64) ([]byte, bool, error)
	// Retire clears committed checkpoints from this trunk's branch-local head.
	Retire() error
}

type xwalTurnJournal struct {
	store  *XwalStore
	ariaID string
}

var _ TurnJournal = (*xwalTurnJournal)(nil)

func (b *XwalBackend) OpenTurnJournal(ariaID string) (TurnJournal, error) {
	if _, err := b.Open(ariaID); err != nil {
		return nil, err
	}
	return &xwalTurnJournal{store: b.store, ariaID: ariaID}, nil
}

func (j *xwalTurnJournal) Checkpoint(targetMainLT uint64, payload []byte) error {
	if targetMainLT == 0 {
		return fmt.Errorf("turn journal: target main LT is zero")
	}
	_, err := j.store.trunks.AppendChannel(j.ariaID, chanTurnWAL, targetMainLT, payload, nil)
	return err
}

func (j *xwalTurnJournal) Sync() error {
	return j.store.trunks.SyncChannel(j.ariaID, chanTurnWAL)
}

func (j *xwalTurnJournal) Latest(targetMainLT uint64) ([]byte, bool, error) {
	r, ok, err := j.store.trunks.LatestChannelRecord(j.ariaID, chanTurnWAL, targetMainLT)
	if err != nil || !ok {
		return nil, false, err
	}
	return append([]byte(nil), r.Payload...), true, nil
}

func (j *xwalTurnJournal) Retire() error {
	j.store.mu.Lock()
	defer j.store.mu.Unlock()
	xw, err := j.store.trunks.Head(j.ariaID)
	if err != nil {
		return err
	}
	defer xw.Close()
	return xw.Clear(chanTurnWAL)
}
