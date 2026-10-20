package xwal

// AppendCursors is Append for the main channel with extra cursor entries; it
// hands back the stamp it wrote so a caller need not read the record.
func (t *Trunks) AppendCursors(trunk string, payload, meta []byte, extra map[string]uint64) (string, uint64, map[string]uint64, error) {
	return t.appendMainWith(trunk, payload, meta, extra)
}

// Append: atMainLT is ignored; appends never fork, ForkAt is the only forking path.
func (t *Trunks) Append(trunk string, atMainLT uint64, payload, meta []byte) (string, uint64, error) {
	_ = atMainLT
	node, lt, _, err := t.appendMainWith(trunk, payload, meta, nil)
	return node, lt, err
}

func (t *Trunks) appendMainWith(trunk string, payload, meta []byte, extra map[string]uint64) (string, uint64, map[string]uint64, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()

	endRead, err := t.beginTrackedRead()
	if err != nil {
		return "", 0, nil, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return "", 0, nil, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	if err != nil {
		return "", 0, nil, err
	}
	lt, cursors, appendErr := x.AppendMainCursors(payload, meta, extra)
	_ = release()
	if appendErr != nil {
		return "", 0, nil, appendErr
	}
	return trunk, lt, cursors, nil
}

// AppendChannel appends to a related channel of a trunk's head.
func (t *Trunks) AppendChannel(trunk, channel string, mainLT uint64, payload, meta []byte) (uint64, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return 0, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return 0, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	if err != nil {
		return 0, err
	}
	defer release()
	if mainLT == 0 {
		mainLT = mainTail(x) + 1 // reducible default: one ahead
	}
	return x.Append(channel, mainLT, payload, meta)
}

// LatestChannelRecord reads from the hot immutable snapshot.
func (t *Trunks) LatestChannelRecord(trunk, channel string, minMainLT uint64) (Record, bool, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return Record{}, false, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return Record{}, false, err
	}
	x, release, err := t.borrowHotUntracked([]string{branch})
	if err != nil {
		return Record{}, false, err
	}
	defer release()
	return x.LatestChannelRecord(channel, minMainLT)
}

// SyncChannelThrough persists one channel of one trunk up to idx. The
// borrow-and-release dance is the same one Append performs, so a writer can
// append a batch and then make exactly that batch durable.
func (t *Trunks) SyncChannelThrough(trunk, channel string, idx uint64) error {
	x, err := t.Head(trunk)
	if err != nil {
		return err
	}
	defer x.Close()
	return x.SyncChannelThrough(channel, idx)
}
