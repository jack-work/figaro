package store

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store/xwal"
)

const modelsStump = "@models"

type segmentBase struct {
	snap    form.Snapshot
	version uint64
}

type compactingStumpLog struct {
	stumpFormLog
	base atomic.Pointer[segmentBase]
}

func openCompactingStumpLog(s *XwalStore, stump string) (*compactingStumpLog, error) {
	if err := s.ensureStump(stump); err != nil {
		return nil, err
	}
	l := &compactingStumpLog{stumpFormLog: stumpFormLog{store: s, stump: stump}}
	x, err := s.trunks.StumpHead(stump)
	if err != nil {
		return nil, err
	}
	defer x.Close()
	_, last, _ := x.ChannelBounds(chanForm)
	if err := l.loadBase(x, last); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *compactingStumpLog) loadBase(x *xwal.XWAL, last uint64) error {
	if last == 0 {
		l.base.Store(&segmentBase{})
		return nil
	}
	header, first, err := x.SegmentHeaderAt(chanForm, last)
	if err != nil {
		return fmt.Errorf("stump %s: segment header: %w", l.stump, err)
	}
	var snap form.Snapshot
	if len(header) > 0 {
		if err := snap.UnmarshalJSON(header); err != nil {
			return fmt.Errorf("stump %s: segment header: %w", l.stump, err)
		}
	}
	if first == 0 {
		first = 1
	}
	l.base.Store(&segmentBase{snap: snap, version: first - 1})
	return nil
}

func (l *compactingStumpLog) Base() (form.Snapshot, uint64) {
	b := l.base.Load()
	return b.snap, b.version
}

func (l *compactingStumpLog) Floor() uint64 { return l.base.Load().version }

func (l *compactingStumpLog) RangePatches(from, upTo uint64, fn func(uint64, []byte) error) error {
	floor := l.Floor()
	if from == 0 {
		from = floor + 1
	}
	if from <= floor {
		return ErrBelowFloor
	}
	return l.stumpFormLog.RangePatches(from, upTo, fn)
}

func (l *compactingStumpLog) SyncThrough(index uint64) error {
	x, err := l.store.trunks.StumpHead(l.stump)
	if err != nil {
		return err
	}
	defer x.Close()
	if err := x.SyncChannelThrough(chanForm, index); err != nil {
		return err
	}
	if err := l.dropSealed(x, index); err != nil {
		slog.Warn("stump form: drop sealed segments", "stump", l.stump, "err", err)
	}
	return nil
}

func (l *compactingStumpLog) dropSealed(x *xwal.XWAL, last uint64) error {
	first, _, ok := x.ChannelBounds(chanForm)
	if !ok || last == 0 {
		return nil
	}
	_, active, err := x.SegmentHeaderAt(chanForm, last)
	if err != nil {
		return err
	}
	if first >= active {
		return nil
	}
	if err := l.loadBase(x, last); err != nil {
		return err
	}
	return x.TruncateFront(chanForm, active)
}

func (b *XwalBackend) ModelsForm() (*Form, error) {
	b.modelsMu.Lock()
	defer b.modelsMu.Unlock()
	if b.models != nil {
		return b.models, nil
	}
	l, err := openCompactingStumpLog(b.store, modelsStump)
	if err != nil {
		return nil, err
	}
	f, err := OpenForm(l)
	if err != nil {
		return nil, err
	}
	b.models = f
	return f, nil
}
