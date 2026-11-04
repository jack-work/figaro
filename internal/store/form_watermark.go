package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

const (
	watermarkSeqsKey      = "system.seqs"
	watermarkSeqsEmptyKey = "system.seqs_empty"
)

var ErrReservedFormKey = fmt.Errorf("%s and %s are reserved for the form log's own bookkeeping", watermarkSeqsKey, watermarkSeqsEmptyKey)

func touchesWatermarkKeys(p form.Patch) bool {
	for _, e := range p.Entries() {
		for _, k := range []string{watermarkSeqsKey, watermarkSeqsEmptyKey} {
			if e.Key == k || strings.HasPrefix(e.Key, k+".") {
				return true
			}
		}
	}
	return false
}

func decodeFormWatermark(raw []byte) (form.Snapshot, map[string]uint64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return form.Snapshot{}, nil, nil
	}
	var snap form.Snapshot
	if err := snap.UnmarshalJSON(raw); err != nil {
		return form.Snapshot{}, nil, err
	}
	key := watermarkSeqsKey
	table, ok := snap.Get(key)
	if !ok {
		key = watermarkSeqsEmptyKey
		if table, ok = snap.Get(key); !ok {
			return snap, nil, nil
		}
	}
	var rows [][2]json.RawMessage
	if err := json.Unmarshal(table, &rows); err != nil {
		return form.Snapshot{}, nil, fmt.Errorf("form watermark seqs: %w", err)
	}
	seqs := make(map[string]uint64, len(rows))
	for _, row := range rows {
		var p struct {
			Key string
			Seq uint64
		}
		if json.Unmarshal(row[0], &p.Key) != nil || json.Unmarshal(row[1], &p.Seq) != nil {
			return form.Snapshot{}, nil, fmt.Errorf("form watermark seqs: bad row %s", row)
		}
		seqs[p.Key] = p.Seq
	}
	snap = snap.DeletePath(key)
	if key == watermarkSeqsEmptyKey {
		snap = snap.SetPath("system", json.RawMessage(`{}`))
	}
	return snap, seqs, nil
}

func encodeFormWatermark(snap form.Snapshot, seqs map[string]uint64) ([]byte, error) {
	if len(seqs) == 0 {
		return snap.MarshalJSON()
	}
	key := watermarkSeqsKey
	if sys, ok := snap.Get("system"); ok {
		switch t := bytes.TrimSpace(sys); {
		case string(t) == "{}":
			key = watermarkSeqsEmptyKey
		case len(t) == 0 || t[0] != '{':
			return snap.MarshalJSON()
		}
	}
	keys := make([]string, 0, len(seqs))
	for k := range seqs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([][2]any, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, [2]any{k, seqs[k]})
	}
	table, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	return snap.SetPath(key, table).MarshalJSON()
}

func formFold(state []byte, records [][]byte) ([]byte, error) {
	snap, seqs, err := decodeFormWatermark(state)
	if err != nil {
		return nil, err
	}
	for _, raw := range records {
		var rec formRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		if k := rec.origin().key(); rec.Seq > 0 && rec.Seq > seqs[k] {
			if seqs == nil {
				seqs = map[string]uint64{}
			}
			seqs[k] = rec.Seq
		}
		snap = snap.Apply(rec.Patch)
	}
	return encodeFormWatermark(snap, seqs)
}
