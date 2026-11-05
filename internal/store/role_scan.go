package store

import (
	"bytes"
	"encoding/json"
	"fmt"
)

var targetAriaToken = []byte(`"` + targetAriaKey + `"`)

func (b *XwalBackend) ScanRoleTargets() (map[string]string, error) {
	out := map[string]string{}
	for _, f := range b.Forms() {
		target, err := b.roleTargetOnDisk(f.ID)
		if err != nil {
			return nil, fmt.Errorf("role scan: form %s: %w", f.ID, err)
		}
		if target != "" {
			out[target] = f.ID
		}
	}
	return out, nil
}

func (b *XwalBackend) roleTargetOnDisk(formID string) (string, error) {
	if _, ok := SourceOfLibretto(formID); ok {
		return "", nil
	}
	b.mu.Lock()
	open := b.forms[formID]
	fault := b.roleScanFault[formID]
	b.mu.Unlock()
	if fault != nil {
		return "", fault
	}
	if open != nil {
		if v := open.Read().Snapshot.Lookup(targetAriaKey); v != nil {
			return *v, nil
		}
		return "", nil
	}
	xw, err := b.store.openNode(formID)
	if err != nil {
		return "", err
	}
	defer xw.Close()
	first, last, _ := xw.ChannelBounds(chanForm)
	if last == 0 {
		return "", nil
	}
	if first == 0 {
		first = 1
	}
	target := ""
	if header, segBase, err := xw.SegmentHeaderAt(chanForm, last); err == nil && segBase > 1 && len(header) > 0 {
		if target, err = watermarkTarget(header); err != nil {
			return "", err
		}
		if segBase > first {
			first = segBase
		}
	}
	for lt := first; lt <= last; lt++ {
		rec, err := xw.ReadAt(chanForm, lt)
		if err != nil {
			return "", err
		}
		if !bytes.Contains(rec.Payload, targetAriaToken) {
			continue
		}
		var r formRecord
		if err := json.Unmarshal(rec.Payload, &r); err != nil {
			return "", err
		}
		if t, spoke := targetOf(r.Patch); spoke {
			target = t
		}
	}
	return target, nil
}

func watermarkTarget(header []byte) (string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(header, &top); err != nil {
		return "", err
	}
	raw, ok := top[targetAriaKey]
	if !ok {
		return "", nil
	}
	return lifetimeString(raw), nil
}

func (b *XwalBackend) StaleRoleIndexForTest(formID, target string) {
	b.roleNoted(formID, target)
	b.dropForm(formID)
}

func (b *XwalBackend) FailRoleScanForTest(formID string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.roleScanFault == nil {
		b.roleScanFault = map[string]error{}
	}
	b.roleScanFault[formID] = err
}
