package xwal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/segment"
)

// reducibleFold adapts a Reducer into figwal's OnSegmentOpen: the new
// watermark is the previous one (or Initial) with every sealed patch folded in.
func reducibleFold(reduce ReduceFunc, initial []byte) func(prevHeader []byte, sealed [][]byte) ([]byte, error) {
	return func(prevHeader []byte, sealed [][]byte) ([]byte, error) {
		state := prevHeader
		if len(state) == 0 {
			state = initial
		}
		for _, f := range sealed {
			_, patch, err := decodeFrame(f)
			if err != nil {
				return nil, err
			}
			state, err = reduce(state, patch)
			if err != nil {
				return nil, err
			}
		}
		return state, nil
	}
}

func seedEmptyLog(dir string, codec segment.SegmentCodec) error {
	final := watermarkPath(dir, 1, codec)
	if pathExists(final) {
		return nil
	}
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	seg, err := segment.Create(tmp, codec, 1, 0)
	if err != nil {
		return err
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, final); err != nil {
		return err
	}
	return disk.SyncDir(dir)
}

func ensureWatermark(chDir string, baseIndex uint64, codec segment.SegmentCodec, initial []byte) error {
	valid, err := validateWatermark(chDir, baseIndex, codec, initial)
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	return rewriteWatermark(chDir, baseIndex, codec, initial)
}

func watermarkPath(chDir string, baseIndex uint64, codec segment.SegmentCodec) string {
	return filepath.Join(chDir, fmt.Sprintf("%020d%s", baseIndex, codec.FileExt()))
}

func firstSegmentBase(dir string, codec segment.SegmentCodec) (uint64, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false, err
	}
	var first uint64
	found := false
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != codec.FileExt() {
			continue
		}
		base, err := strconv.ParseUint(strings.TrimSuffix(entry.Name(), codec.FileExt()), 10, 64)
		if err != nil {
			continue
		}
		if !found || base < first {
			first = base
			found = true
		}
	}
	return first, found, nil
}

func validateWatermark(
	chDir string,
	baseIndex uint64,
	codec segment.SegmentCodec,
	expected []byte,
) (bool, error) {
	path := watermarkPath(chDir, baseIndex, codec)
	if err := recoverAtomicReplacement(path); err != nil {
		return false, err
	}
	if !pathExists(path) {
		return false, nil
	}
	header, _, ok, err := readWatermarkSegment(path, codec)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return statesEqual(header, expected), nil
}

func statesEqual(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	var l, r any
	ld := json.NewDecoder(bytes.NewReader(left))
	rd := json.NewDecoder(bytes.NewReader(right))
	ld.UseNumber()
	rd.UseNumber()
	if ld.Decode(&l) != nil || rd.Decode(&r) != nil {
		return false
	}
	lc, lerr := json.Marshal(l)
	rc, rerr := json.Marshal(r)
	return lerr == nil && rerr == nil && bytes.Equal(lc, rc)
}

func readWatermarkSegment(path string, codec segment.SegmentCodec) ([]byte, uint64, bool, error) {
	header, entries, ok, err := readHeaderedSegment(path, codec)
	return header, uint64(len(entries)), ok, err
}

func readHeaderedSegment(
	path string,
	codec segment.SegmentCodec,
) ([]byte, [][]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, false, err
	}
	defer file.Close()
	type span struct {
		off int64
		len int
	}
	var spans []span
	if err := codec.ScanFrames(file, func(off int64, frameLen int) error {
		spans = append(spans, span{off: off, len: frameLen})
		return nil
	}); err != nil {
		return nil, nil, false, err
	}
	if len(spans) == 0 {
		return nil, nil, false, nil
	}
	frames := make([][]byte, 0, len(spans))
	for _, span := range spans {
		payload, _, err := codec.ReadFrame(file, span.off, span.off+int64(span.len))
		if err != nil {
			return nil, nil, false, err
		}
		frames = append(frames, payload)
	}
	return frames[0], frames[1:], true, nil
}

func rewriteWatermark(
	chDir string,
	baseIndex uint64,
	codec segment.SegmentCodec,
	header []byte,
) error {
	final := watermarkPath(chDir, baseIndex, codec)
	var entries [][]byte
	if pathExists(final) {
		_, existing, readable, err := readHeaderedSegment(final, codec)
		if err != nil {
			return err
		}
		if readable {
			entries = existing
		}
	}
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	seg, err := segment.Create(tmp, codec, baseIndex, 0)
	if err != nil {
		return err
	}
	if err := seg.WriteHeader(header); err != nil {
		seg.Close()
		return err
	}
	for _, entry := range entries {
		if _, err := seg.Append(entry); err != nil {
			seg.Close()
			return err
		}
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, final); err != nil {
		return err
	}
	return disk.SyncDir(chDir)
}

// seedWatermark writes a header-only segment carrying Initial, so an empty
// reducible node folds from a defined watermark.
func seedWatermark(chDir string, baseIndex uint64, codec segment.SegmentCodec, initial []byte) error {
	final := watermarkPath(chDir, baseIndex, codec)
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	seg, err := segment.Create(tmp, codec, baseIndex, 0)
	if err != nil {
		return err
	}
	if err := seg.WriteHeader(initial); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Close(); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, final); err != nil {
		return err
	}
	return disk.SyncDir(chDir)
}
