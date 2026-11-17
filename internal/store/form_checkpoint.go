package store

import (
	"log/slog"
	"time"
)

func (f *Form) maybeCheckpoint() {
	cl, ok := f.log.(checkpointLog)
	if !ok {
		return
	}
	st := f.state.Load()
	if !checkpointPolicyOf(st.snap).due(st.version, f.ckptLast.Load(), time.UnixMilli(f.ckptLastAt.Load()), time.Now()) {
		return
	}
	if !f.ckptBusy.CompareAndSwap(false, true) {
		return
	}
	f.ckptGate.Lock()
	if f.closed.Load() {
		f.ckptGate.Unlock()
		f.ckptBusy.Store(false)
		return
	}
	f.ckpt.Add(1)
	f.ckptGate.Unlock()
	go func() {
		defer f.ckpt.Done()
		defer f.ckptBusy.Store(false)
		for !f.closed.Load() {
			st := f.state.Load()
			pol := checkpointPolicyOf(st.snap)
			if !pol.due(st.version, f.ckptLast.Load(), time.UnixMilli(f.ckptLastAt.Load()), time.Now()) {
				return
			}
			if err := f.checkpointAt(cl, st, pol); err != nil {
				slog.Warn("form checkpoint", "version", st.version, "err", err)
				return
			}
		}
	}()
}

func (f *Form) checkpointAt(cl checkpointLog, st *formState, pol checkpointPolicy) error {
	body, err := encodeFormWatermark(st.snap, st.seqs)
	if err != nil {
		return err
	}
	crc, err := cl.RecordCRC(st.version)
	if err != nil {
		return err
	}
	if err := cl.WriteCheckpoint(st.version, crc, body); err != nil {
		return err
	}
	now := time.Now()
	evicted, err := f.editMarks(cl, pol.maxMarks, func(marks map[uint64]Mark) {
		for v, m := range marks {
			if m.Pin == PinTail {
				m.Pin = ""
				marks[v] = m
			}
		}
		marks[st.version] = Mark{Pin: PinTail, Used: now.UnixMilli()}
	})
	if err != nil {
		return err
	}
	f.ckptLast.Store(st.version)
	f.ckptLastAt.Store(now.UnixMilli())
	for _, v := range evicted {
		if err := cl.RemoveCheckpoint(v); err != nil {
			slog.Warn("form checkpoint: remove evicted", "version", v, "err", err)
		}
	}
	return nil
}

func (f *Form) editMarks(cl checkpointLog, maxMarks int, edit func(map[uint64]Mark)) ([]uint64, error) {
	f.ckptMu.Lock()
	defer f.ckptMu.Unlock()
	held := map[uint64]bool{}
	for _, v := range cl.CheckpointVersions() {
		held[v] = true
	}
	at := f.Read()
	marks := MarksOf(at.Snapshot)
	if marks == nil {
		marks = map[uint64]Mark{}
	}
	for v := range marks {
		if !held[v] {
			delete(marks, v)
		}
	}
	edit(marks)
	evicted := evictMarks(marks, maxMarks)
	for v := range held {
		if _, ok := marks[v]; !ok {
			evicted = append(evicted, v)
		}
	}
	p, err := marksPatch(at.Snapshot, marks)
	if err != nil {
		return nil, err
	}
	if _, _, err := f.applyEffect(p, 0, Ensure, true); err != nil {
		return nil, err
	}
	return dedupe(evicted), nil
}

func dedupe(vs []uint64) []uint64 {
	seen := map[uint64]bool{}
	out := vs[:0]
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
