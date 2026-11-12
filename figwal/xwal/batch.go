package xwal

import (
	"errors"
	"fmt"

	"github.com/jack-work/figaro/figwal/log"
)

type ChannelRow struct {
	MainLT  uint64
	Payload []byte
	Meta    []byte
}

func (t *Trunks) AppendChannelBatch(trunk, channel string, rows []ChannelRow) (uint64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	first, onDisk, err := t.appendChannelBatchLocked(trunk, channel, rows)
	if err == nil {
		return first, nil
	}
	if onDisk {
		cut := func(x *XWAL) error { return x.TruncateFrom(channel, first) }
		if cerr := t.cutChannelLocked(trunk, channel, cut); cerr != nil {
			return 0, fmt.Errorf("%w; rows from %d could not be taken back: %w", err, first, cerr)
		}
	}
	return 0, err
}

func (t *Trunks) appendChannelBatchLocked(trunk, channel string, rows []ChannelRow) (first uint64, onDisk bool, err error) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return 0, false, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return 0, false, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	if err != nil {
		return 0, false, err
	}
	defer release()

	var last uint64
	for _, r := range rows {
		last, err = x.Append(channel, r.MainLT, r.Payload, r.Meta)
		if err != nil {
			break
		}
		if first == 0 {
			first = last
		}
	}
	if err == nil {
		sync := func() error { return x.SyncChannelThrough(channel, last) }
		if t.testBatchSync != nil {
			err = t.testBatchSync(sync)
		} else {
			err = sync()
		}
	}
	if err == nil || first == 0 {
		return first, false, err
	}
	switch derr := x.DiscardFrom(channel, first); {
	case derr == nil:
		return first, false, err
	case errors.Is(derr, log.ErrSharedMutation):
		return first, true, err
	default:
		return first, false, fmt.Errorf("%w; rows from %d could not be taken back: %w", err, first, derr)
	}
}

func (t *Trunks) BatchSyncForTest(wrap func(sync func() error) error) {
	t.testBatchSync = wrap
}

func (s *Store) AppendBatch(trunk, channel string, rows []ChannelRow) (uint64, error) {
	if err := s.poisoned(trunk); err != nil {
		return 0, err
	}
	first, err := s.Trunks.AppendChannelBatch(trunk, channel, rows)
	if errors.Is(err, ErrNoChannel) {
		if cerr := s.autoCreateChannel(channel); cerr != nil {
			return 0, cerr
		}
		first, err = s.Trunks.AppendChannelBatch(trunk, channel, rows)
	}
	if err != nil {
		return 0, err
	}
	s.markDirty(trunk)
	return first, nil
}
