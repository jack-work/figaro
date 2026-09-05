package xwal

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/segment"
)

const uncleanName = ".unclean"

func uncleanPath(root string) string { return filepath.Join(root, uncleanName) }

func markUnclean(root string) error {
	if err := writeSyncedFile(uncleanPath(root), []byte("open\n")); err != nil {
		return err
	}
	return disk.SyncDir(root)
}

func clearUnclean(root string) error {
	if err := os.Remove(uncleanPath(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return disk.SyncDir(root)
}

// repairCoherentCuts trims every live leaf lineage to a coherent cut: a
// related-channel record whose main-LT referent did not survive the
// crash is dropped, so no record references a missing main entry and no
// reducible watermark runs ahead of main. Idempotent.
func repairCoherentCuts(t *Trunks) error {
	man, err := loadOrCreateManifest(t.root, t.cfg)
	if err != nil {
		return err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return err
	}
	kids := t.idx.ChildIndex()
	for key := range t.idx.All() {
		if len(kids[key]) > 0 {
			continue
		}
		mainTail, err := nodeMainTail(t.irDir(key), codec)
		if err != nil {
			return fmt.Errorf("xwal: repair leaf %q: %w", key, err)
		}
		for _, mc := range man.Channels {
			if mc.Name == man.Main {
				continue
			}
			dir := filepath.Join(t.root, mc.Name, fsName(key))
			if !pathExists(dir) {
				continue
			}
			trimmed, err := trimChannelNode(dir, codec, mc.Kind == ChannelReducible.String(), mainTail)
			if err != nil {
				return fmt.Errorf("xwal: repair channel %q leaf %q: %w", mc.Name, key, err)
			}
			if trimmed > 0 {
				slog.Warn("xwal: trimmed incoherent channel tail",
					"channel", mc.Name, "leaf", key, "records", trimmed, "mainTail", mainTail)
			}
		}
	}
	return nil
}

type frameSpan struct {
	off int64
	len int
}

func scanSegmentFrames(path string, codec segment.SegmentCodec) ([]frameSpan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var spans []frameSpan
	if err := codec.ScanFrames(f, func(off int64, frameLen int) error {
		spans = append(spans, frameSpan{off: off, len: frameLen})
		return nil
	}); err != nil {
		return nil, err
	}
	return spans, nil
}

func readFrameMainLT(path string, codec segment.SegmentCodec, span frameSpan) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	payload, _, err := codec.ReadFrame(f, span.off, span.off+int64(span.len))
	if err != nil {
		return 0, err
	}
	return decodeMainLT(payload)
}

func segmentBases(dir string, codec segment.SegmentCodec) ([]uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var bases []uint64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), codec.FileExt()) {
			continue
		}
		base, err := strconv.ParseUint(strings.TrimSuffix(e.Name(), codec.FileExt()), 10, 64)
		if err != nil {
			continue
		}
		bases = append(bases, base)
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i] < bases[j] })
	return bases, nil
}

// nodeMainTail is the last complete main-channel entry index in a node
// dir, or forkBase-1 for an empty fork node, or 0 for an empty root.
func nodeMainTail(dir string, codec segment.SegmentCodec) (uint64, error) {
	bases, err := segmentBases(dir, codec)
	if err != nil {
		return 0, err
	}
	for i := len(bases) - 1; i >= 0; i-- {
		spans, err := scanSegmentFrames(filepath.Join(dir, segFileName(bases[i], codec)), codec)
		if err != nil {
			return 0, err
		}
		if len(spans) > 0 {
			return bases[i] + uint64(len(spans)) - 1, nil
		}
	}
	base, err := readForkBaseFile(filepath.Join(dir, ".fork"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return base - 1, nil
}

func segFileName(base uint64, codec segment.SegmentCodec) string {
	return fmt.Sprintf("%020d%s", base, codec.FileExt())
}

// trimChannelNode drops the suffix of a related channel node whose
// records reference main-LTs beyond mainTail, or beyond mainTail+1 for
// reducible channels, whose one-ahead patch convention keys a patch to
// the upcoming turn; such a patch survives a crash by contract. Records
// are main-LT non-decreasing, so the violating region is a contiguous
// tail.
func trimChannelNode(dir string, codec segment.SegmentCodec, headered bool, mainTail uint64) (int, error) {
	if headered {
		mainTail++
	}
	bases, err := segmentBases(dir, codec)
	if err != nil {
		return 0, err
	}
	trimmed := 0
	for i := len(bases) - 1; i >= 0; i-- {
		path := filepath.Join(dir, segFileName(bases[i], codec))
		spans, err := scanSegmentFrames(path, codec)
		if err != nil {
			return trimmed, err
		}
		entryStart := 0
		if headered && len(spans) > 0 {
			entryStart = 1
		}
		keep := len(spans)
		for j := len(spans) - 1; j >= entryStart; j-- {
			m, err := readFrameMainLT(path, codec, spans[j])
			if err != nil {
				return trimmed, err
			}
			if m <= mainTail {
				break
			}
			keep = j
		}
		if keep == len(spans) {
			if len(spans) > entryStart {
				return trimmed, nil
			}
			continue
		}
		trimmed += len(spans) - keep
		if keep == entryStart && !headered && i > 0 {
			if err := os.Remove(path); err != nil {
				return trimmed, err
			}
			if err := disk.SyncDir(dir); err != nil {
				return trimmed, err
			}
			continue
		}
		cut := int64(0)
		if keep > 0 {
			cut = spans[keep-1].off + int64(spans[keep-1].len)
		}
		if err := truncateFileSynced(path, cut); err != nil {
			return trimmed, err
		}
		if keep > entryStart {
			return trimmed, nil
		}
	}
	return trimmed, nil
}

func truncateFileSynced(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
