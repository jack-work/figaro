package disk

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

var ErrCompacted = errors.New("disk log: index lies in a dropped segment")

const holeSuffix = ".hole"

func (l *Log) holeName(base uint64) string { return l.segName(base) + holeSuffix }

func isHoleName(name, ext string) (string, bool) {
	seg, ok := strings.CutSuffix(name, holeSuffix)
	return seg, ok && strings.HasSuffix(seg, ext)
}

type SealedSpan struct {
	First, Last uint64
	Bytes       int64
}

func (l *Log) droppableLocked(keep func(first, last uint64) bool) []*sealedSeg {
	var out []*sealedSeg
	for i, ss := range l.sealed {
		if ss.hole || i == len(l.sealed)-1 || keep(ss.base, ss.last) {
			continue
		}
		out = append(out, ss)
	}
	return out
}

func spanOf(ss *sealedSeg) SealedSpan {
	sp := SealedSpan{First: ss.base, Last: ss.last}
	if fi, err := os.Stat(ss.path); err == nil {
		sp.Bytes = fi.Size()
	}
	return sp
}

func (l *Log) Droppable(keep func(first, last uint64) bool) []SealedSpan {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []SealedSpan
	for _, ss := range l.droppableLocked(keep) {
		out = append(out, spanOf(ss))
	}
	return out
}

func (l *Log) DropSealed(keep func(first, last uint64) bool) ([]SealedSpan, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var dropped []SealedSpan
	for _, ss := range l.droppableLocked(keep) {
		sp := spanOf(ss)
		if err := l.holeOutLocked(ss); err != nil {
			return dropped, err
		}
		dropped = append(dropped, sp)
	}
	front := 0
	for l.forkBase == 0 && front < len(l.sealed) && l.sealed[front].hole {
		if err := os.Remove(filepath.Join(l.dir, l.holeName(l.sealed[front].base))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return dropped, err
		}
		front++
	}
	if front > 0 {
		l.sealed = append([]*sealedSeg(nil), l.sealed[front:]...)
		if err := syncDir(l.dir); err != nil {
			return dropped, err
		}
	}
	if len(dropped) > 0 {
		slog.Info("log segments dropped", "dir", l.dir, "dropped", len(dropped), "sealed", len(l.sealed))
	}
	return dropped, nil
}

func (l *Log) Floor() uint64 {
	return l.floorBelow(^uint64(0))
}

func (l *Log) floorBelow(limit uint64) uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var floor uint64
	if l.parent != nil && l.forkBase > 1 {
		floor = l.parent.floorBelow(min(limit, l.forkBase))
	}
	below := func(idx uint64) {
		floor = max(floor, min(idx, limit-1))
	}
	start := max(l.forkBase, 1)
	if first := l.firstIndexLocked(); first > start && first <= limit {
		below(first - 1)
	}
	for _, ss := range l.sealed {
		if ss.hole && ss.base < limit {
			below(ss.last)
		}
	}
	return floor
}

func (l *Log) holeOutLocked(ss *sealedSeg) error {
	stub := filepath.Join(l.dir, l.holeName(ss.base))
	f, err := os.OpenFile(stub, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("hole stub for segment %d: %w", ss.base, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := syncDir(l.dir); err != nil {
		return err
	}
	if err := ss.close(); err != nil {
		return err
	}
	if err := os.Remove(ss.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ss.hole = true
	return syncDir(l.dir)
}

func SegmentFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for _, ext := range knownExts {
			if strings.HasSuffix(e.Name(), ext) {
				n++
				break
			}
		}
	}
	return n, nil
}

func ForkBaseOf(dir string) (uint64, error) { return readForkMarker(dir) }

func StubHole(dir, segFile string) error {
	f, err := os.OpenFile(filepath.Join(dir, segFile+holeSuffix), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(dir)
}
