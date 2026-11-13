package xwal

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/figwal/disk"
	"github.com/jack-work/figaro/figwal/segment"
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

func repairCoherentCuts(t *Trunks) error {
	plan, err := loadRepairPlan(t.root, t.cfg)
	if err != nil {
		return err
	}
	for _, key := range suspectLeaves(t.idx) {
		if _, err := plan.repairLeaf(t, key); err != nil {
			return err
		}
	}
	return nil
}

type repairPlan struct {
	codec segment.SegmentCodec
	keyed []keyedChannel
}

func loadRepairPlan(root string, cfg Config) (*repairPlan, error) {
	man, err := loadOrCreateManifest(root, cfg)
	if err != nil {
		return nil, err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return nil, err
	}
	keyed, err := keyedChannelNodes(root, man)
	if err != nil {
		return nil, err
	}
	return &repairPlan{codec: codec, keyed: keyed}, nil
}

func suspectLeaves(idx *Index) []string {
	kids := idx.ChildIndex()
	var out []string
	for key := range idx.All() {
		if len(kids[key]) == 0 {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (p *repairPlan) repairLeaf(t *Trunks, key string) (int, error) {
	suspects := leafSuspects(t.root, p.keyed, key, p.codec)
	if len(suspects) == 0 {
		return 0, nil
	}
	mainTail, err := nodeMainTail(t.irDir(key), p.codec)
	if errors.Is(err, segment.ErrCorrupt) {
		quarantine(key, err)
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("xwal: repair leaf %q: %w", key, err)
	}
	total := 0
	for _, s := range suspects {
		trimmed, err := trimChannelNode(s.dir, p.codec, s.reducible, mainTail)
		total += trimmed
		if errors.Is(err, segment.ErrCorrupt) {
			quarantine(key, err)
			break
		}
		if err != nil {
			return total, fmt.Errorf("xwal: repair channel %q leaf %q: %w", s.channel, key, err)
		}
		if trimmed > 0 {
			slog.Warn("xwal: trimmed incoherent channel tail",
				"channel", s.channel, "leaf", key, "records", trimmed, "mainTail", mainTail)
		}
	}
	return total, nil
}

type keyedChannel struct {
	name      string
	reducible bool
	nodes     map[string]bool
}

type suspectChannel struct {
	channel   string
	dir       string
	reducible bool
}

func keyedChannelNodes(root string, man manifest) ([]keyedChannel, error) {
	var out []keyedChannel
	for _, mc := range man.Channels {
		if mc.Name == man.Main || mc.Unkeyed {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, mc.Name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("xwal: repair channel %q: %w", mc.Name, err)
		}
		nodes := make(map[string]bool, len(entries))
		for _, e := range entries {
			nodes[e.Name()] = true
		}
		out = append(out, keyedChannel{name: mc.Name, reducible: mc.Kind == ChannelReducible.String(), nodes: nodes})
	}
	return out, nil
}

func leafSuspects(root string, keyed []keyedChannel, key string, codec segment.SegmentCodec) []suspectChannel {
	name := fsName(key)
	var out []suspectChannel
	for _, kc := range keyed {
		dir := filepath.Join(root, kc.name, name)
		if name == "" {
			if !pathExists(dir) {
				continue
			}
		} else if !kc.nodes[name] {
			continue
		}
		if !holdsFrames(dir, codec) {
			continue
		}
		out = append(out, suspectChannel{channel: kc.name, dir: dir, reducible: kc.reducible})
	}
	return out
}

func holdsFrames(dir string, codec segment.SegmentCodec) bool {
	bases, err := segmentBases(dir, codec)
	if err != nil {
		return true
	}
	for _, base := range bases {
		fi, err := os.Stat(filepath.Join(dir, segFileName(base, codec)))
		if err != nil || fi.Size() > 0 {
			return true
		}
	}
	return false
}

func quarantine(leaf string, err error) {
	slog.Error("xwal: leaf left unrepaired: a corrupt frame has valid records after it, so nothing past it is trimmed",
		"leaf", leaf, "err", err)
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
		return nil, fmt.Errorf("%s: %w", path, err)
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
	return decodeMainLT(payload, verifiesReads(codec))
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

func nodeMainTail(dir string, codec segment.SegmentCodec) (uint64, error) {
	bases, err := segmentBases(dir, codec)
	if err != nil {
		return 0, err
	}
	for i := len(bases) - 1; i >= 0; i-- {
		path := filepath.Join(dir, segFileName(bases[i], codec))
		if readsTailOnly(codec) {
			idx, found, stamped, err := lastFrameIdx(path, codec)
			if err != nil {
				return 0, err
			}
			if !found {
				continue
			}
			if stamped && idx >= bases[i] {
				return idx, nil
			}
		}
		spans, err := scanSegmentFrames(path, codec)
		if err != nil {
			return 0, err
		}
		if len(spans) > 0 {
			return bases[i] + uint64(len(spans)) - 1, nil
		}
	}
	base, err := readForkBaseStrict(dir)
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

func trimChannelNode(dir string, codec segment.SegmentCodec, headered bool, mainTail uint64) (int, error) {
	if !readsTailOnly(codec) {
		return trimChannelNodeScan(dir, codec, headered, mainTail)
	}
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
		cut, n, sawEntry, allGone, err := judgeTail(path, codec, headered, mainTail)
		if err != nil {
			return trimmed, err
		}
		if n == 0 {
			if sawEntry {
				return trimmed, nil
			}
			continue
		}
		trimmed += n
		if allGone && !headered && i > 0 {
			if err := os.Remove(path); err != nil {
				return trimmed, err
			}
			if err := disk.SyncDir(dir); err != nil {
				return trimmed, err
			}
			continue
		}
		if err := truncateFileSynced(path, cut); err != nil {
			return trimmed, err
		}
		if !allGone {
			return trimmed, nil
		}
	}
	return trimmed, nil
}

func trimChannelNodeScan(dir string, codec segment.SegmentCodec, headered bool, mainTail uint64) (int, error) {
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

var repairReadsTailOnly = true

func readsTailOnly(codec segment.SegmentCodec) bool {
	_, jsonl := codec.(segment.JSONLCodec)
	return repairReadsTailOnly && jsonl
}

func lastFrameIdx(path string, codec segment.SegmentCodec) (idx uint64, found, stamped bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, false, err
	}
	defer f.Close()
	lines, err := newBackwardLines(f)
	if err != nil {
		return 0, false, false, fmt.Errorf("%s: %w", path, err)
	}
	for {
		_, line, ok, err := lines.prev()
		if err != nil {
			return 0, false, false, fmt.Errorf("%s: %w", path, err)
		}
		if !ok {
			return 0, false, false, nil
		}
		idx, stamped := canonicalLineIdx(line)
		if _, _, err := codec.ReadFrame(bytes.NewReader(line), 0, int64(len(line))); err != nil {
			continue
		}
		return idx, true, stamped, nil
	}
}

func judgeTail(path string, codec segment.SegmentCodec, headered bool, mainTail uint64) (cut int64, trimmed int, sawEntry, allGone bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false, false, err
	}
	defer f.Close()
	lines, err := newBackwardLines(f)
	if err != nil {
		return 0, 0, false, false, fmt.Errorf("%s: %w", path, err)
	}
	sawValid := false
	for {
		off, line, ok, err := lines.prev()
		if err != nil {
			return 0, 0, false, false, fmt.Errorf("%s: %w", path, err)
		}
		if !ok {
			return cut, trimmed, sawEntry, true, nil
		}
		payload, _, err := codec.ReadFrame(bytes.NewReader(line), 0, int64(len(line)))
		if err != nil {
			if sawValid {
				return 0, 0, false, false, fmt.Errorf("%s: %w", path, &segment.CorruptFrameError{Offset: off})
			}
			continue
		}
		sawValid = true
		if headered && off == 0 {
			return cut, trimmed, sawEntry, true, nil
		}
		sawEntry = true
		m, err := decodeMainLT(payload, verifiesReads(codec))
		if err != nil {
			return 0, 0, false, false, err
		}
		if m <= mainTail {
			return cut, trimmed, sawEntry, false, nil
		}
		trimmed++
		cut = off
	}
}

const (
	canonicalHashPrefix = `{"_hash":"`
	canonicalIdxPrefix  = `","_idx":`
)

func canonicalLineIdx(line []byte) (uint64, bool) {
	if !bytes.HasPrefix(line, []byte(canonicalHashPrefix)) {
		return 0, false
	}
	rest := line[len(canonicalHashPrefix):]
	i := bytes.Index(rest, []byte(canonicalIdxPrefix))
	if i <= 0 {
		return 0, false
	}
	for _, c := range rest[:i] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return 0, false
		}
	}
	digits := rest[i+len(canonicalIdxPrefix):]
	n := 0
	for n < len(digits) && digits[n] >= '0' && digits[n] <= '9' {
		n++
	}
	if n == 0 || n == len(digits) || (digits[n] != ',' && digits[n] != '}') {
		return 0, false
	}
	idx, err := strconv.ParseUint(string(digits[:n]), 10, 64)
	if err != nil {
		return 0, false
	}
	return idx, true
}

const backwardChunk = 64 << 10

type backwardLines struct {
	f      *os.File
	bufOff int64
	buf    []byte
	end    int64
}

func newBackwardLines(f *os.File) (*backwardLines, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b := &backwardLines{f: f, bufOff: fi.Size()}
	p, err := b.lastNewlineBefore(fi.Size())
	if err != nil {
		return nil, err
	}
	b.end = p + 1
	return b, nil
}

func (b *backwardLines) prev() (int64, []byte, bool, error) {
	if b.end == 0 {
		return 0, nil, false, nil
	}
	p, err := b.lastNewlineBefore(b.end - 1)
	if err != nil {
		return 0, nil, false, err
	}
	start := p + 1
	line := b.buf[start-b.bufOff : b.end-b.bufOff]
	b.end = start
	return start, line, true, nil
}

func (b *backwardLines) lastNewlineBefore(limit int64) (int64, error) {
	for {
		if limit > b.bufOff {
			if i := bytes.LastIndexByte(b.buf[:limit-b.bufOff], '\n'); i >= 0 {
				return b.bufOff + int64(i), nil
			}
		}
		if b.bufOff == 0 {
			return -1, nil
		}
		n := int64(backwardChunk)
		if l := int64(len(b.buf)); l > n {
			n = l
		}
		if n > b.bufOff {
			n = b.bufOff
		}
		chunk := make([]byte, n+int64(len(b.buf)))
		if _, err := b.f.ReadAt(chunk[:n], b.bufOff-n); err != nil {
			return 0, err
		}
		copy(chunk[n:], b.buf)
		b.buf = chunk
		b.bufOff -= n
	}
}
