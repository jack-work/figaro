package xwal

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Read returns the (mainLT, payload) at channelLT — the meta-free view.
func (x *XWAL) Read(channelName string, channelLT uint64) (uint64, []byte, error) {
	r, err := x.ReadAt(channelName, channelLT)
	if err != nil {
		return 0, nil, err
	}
	return r.MainLT, r.Payload, nil
}

func (x *XWAL) ReadAt(channelName string, channelLT uint64) (Record, error) {
	ch := x.channel(channelName)
	if ch == nil {
		return Record{}, fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return Record{}, err
	}
	f, err := l.Read(channelLT)
	if err != nil {
		return Record{}, err
	}
	return decodeRecordFrom(channelLT, f, channelName == x.main)
}

// Lookup is the foreign-key view; the last entry wins if several share the main LT.
func (x *XWAL) Lookup(channelName string, mainLT uint64) (Record, bool, error) {
	ch := x.channel(channelName)
	if ch == nil {
		return Record{}, false, fmt.Errorf("xwal: no channel %q", channelName)
	}
	// The main channel is identity (main-LT == channel-LT), so it needs no fk index.
	if channelName == x.main {
		l, err := ch.Log()
		if err != nil {
			return Record{}, false, err
		}
		first, last := l.FirstIndex(), l.LastIndex()
		if first == 0 || mainLT < first || mainLT > last {
			return Record{}, false, nil
		}
		r, err := x.ReadAt(channelName, mainLT)
		if err != nil {
			return Record{}, false, err
		}
		return r, true, nil
	}
	lt, ok, err := ch.lookup(mainLT)
	if err != nil {
		return Record{}, false, err
	}
	if !ok {
		return Record{}, false, nil
	}
	r, err := x.ReadAt(channelName, lt)
	if err != nil {
		return Record{}, false, err
	}
	return r, true, nil
}

// RecordsFrom walks the snapshot backward only to locate the boundary, then
// reads the ascending delta without building a total-history index. limit 0
// returns every matching record.
func (x *XWAL) RecordsFrom(channelName string, fromMainLT uint64, limit int) ([]Record, error) {
	if limit < 0 {
		return nil, fmt.Errorf("xwal: negative record limit %d", limit)
	}
	ch := x.channel(channelName)
	if ch == nil {
		return nil, fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, oerr := ch.Log()
	if oerr != nil {
		return nil, oerr
	}
	snapshot := l.Snapshot()

	var first uint64
	err := snapshot.ScanFromEnd(0, func(idx uint64, frame []byte) error {
		mainLT, err := decodeMainLT(frame)
		if err != nil {
			return err
		}
		if mainLT < fromMainLT {
			return errStopRange
		}
		first = idx
		return nil
	})
	if err != nil && !errors.Is(err, errStopRange) {
		return nil, err
	}
	if first == 0 {
		return nil, nil
	}

	records := make([]Record, 0)
	err = snapshot.Range(first, func(idx uint64, frame []byte) error {
		record, err := decodeRecordFrom(idx, frame, channelName == x.main)
		if err != nil {
			return err
		}
		if record.MainLT < fromMainLT {
			return nil
		}
		records = append(records, record)
		if limit > 0 && len(records) == limit {
			return errStopRange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopRange) {
		return nil, err
	}
	return records, nil
}

// LatestChannelRecord is the newest record when its main LT meets minMainLT.
func (x *XWAL) LatestChannelRecord(channelName string, minMainLT uint64) (Record, bool, error) {
	ch := x.channel(channelName)
	if ch == nil {
		return Record{}, false, fmt.Errorf("xwal: no channel %q", channelName)
	}
	l, oerr := ch.Log()
	if oerr != nil {
		return Record{}, false, oerr
	}
	var latest Record
	found := false
	err := l.Snapshot().ScanFromEnd(0, func(idx uint64, frame []byte) error {
		record, err := decodeRecordFrom(idx, frame, channelName == x.main)
		if err != nil {
			return err
		}
		if record.MainLT >= minMainLT {
			latest = record
			found = true
		}
		return errStopRange
	})
	if err != nil && !errors.Is(err, errStopRange) {
		return Record{}, false, err
	}
	return latest, found, nil
}

// StateAt folds a reducible channel to channelLT (watermark + patches).
func (x *XWAL) StateAt(channelName string, channelLT uint64) ([]byte, error) {
	ch := x.channel(channelName)
	if ch == nil {
		return nil, fmt.Errorf("xwal: no channel %q", channelName)
	}
	if ch.kind != ChannelReducible {
		return nil, fmt.Errorf("xwal: channel %q is not reducible", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return nil, err
	}
	if l.Absent() {
		return append([]byte(nil), ch.initial...), nil
	}
	return l.StateAt(channelLT)
}

// SegmentHeaderAt returns the header and the segment's base index from one
// segment under one lock, walking the parent chain as StateAt does.
func (x *XWAL) SegmentHeaderAt(channelName string, channelLT uint64) ([]byte, uint64, error) {
	ch := x.channel(channelName)
	if ch == nil {
		return nil, 0, fmt.Errorf("xwal: no channel %q", channelName)
	}
	if ch.kind != ChannelReducible {
		return nil, 0, fmt.Errorf("xwal: channel %q is not reducible", channelName)
	}
	l, err := ch.Log()
	if err != nil {
		return nil, 0, err
	}
	if l.Absent() {
		return append([]byte(nil), ch.initial...), 1, nil
	}
	return l.SegmentHeaderAt(channelLT)
}

func (x *XWAL) tailMain(ch *channel) (uint64, bool, error) {
	l, err := ch.Log()
	if err != nil {
		return 0, false, err
	}
	last := l.LastIndex()
	first := l.FirstIndex()
	if first == 0 || last < first {
		return 0, false, nil
	}
	f, err := l.Read(last)
	if err != nil {
		return 0, false, err
	}
	m, err := decodeMainLT(f)
	return m, true, err
}

// CursorAt is where an unkeyed channel stood when main record `at` was
// written, from that record's cursor stamp.
func (x *XWAL) CursorAt(at uint64, channel string) (uint64, error) {
	if at == 0 {
		return 0, nil
	}
	mainLog, err := x.channel(x.main).Log()
	if err != nil {
		return 0, err
	}
	frame, err := mainLog.Read(at)
	if err != nil {
		return 0, fmt.Errorf("xwal: cursor for %q at main %d: %w", channel, at, err)
	}
	var o frameObj
	if err := json.Unmarshal(frame, &o); err != nil {
		return 0, fmt.Errorf("xwal: decode main %d: %w", at, err)
	}
	if v, ok := o.C[channel]; ok {
		return v, nil
	}
	// Migration-era only: a store written before the cursor stamp still keys
	// its related records by main LT, so the boundary is recoverable by
	// lookup. A store this build created writes main LT 0 on unkeyed
	// records, so the lookup would hand a fork records written after it branched.
	if !x.unstampedRecords {
		return 0, nil
	}
	lt, ok, lerr := x.channel(channel).lookupAtOrBelow(at)
	if lerr != nil {
		return 0, lerr
	}
	if !ok {
		return 0, nil
	}
	return lt, nil
}
