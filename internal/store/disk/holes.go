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

func (l *Log) DropSealed(keep func(first, last uint64) bool) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	dropped := 0
	for i, ss := range l.sealed {
		if ss.hole || i == len(l.sealed)-1 || keep(ss.base, ss.last) {
			continue
		}
		if err := l.holeOutLocked(ss); err != nil {
			return dropped, err
		}
		dropped++
	}
	front := 0
	for front < len(l.sealed) && l.sealed[front].hole {
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
	if dropped > 0 {
		slog.Info("log segments dropped", "dir", l.dir, "dropped", dropped, "sealed", len(l.sealed))
	}
	return dropped, nil
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
