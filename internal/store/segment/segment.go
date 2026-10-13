package segment

import (
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/store/tree"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

var (
	ErrFull       = errors.New("segment full")
	ErrOutOfRange = errors.New("index out of range")
	ErrReadOnly   = errors.New("segment is read-only")
)

// Segment is one append-only file framed by a SegmentCodec.
type Segment struct {
	f         *os.File
	path      string
	codec     SegmentCodec
	baseIndex uint64
	size      int64
	maxSize   int64
	count     uint64
	offsets   []int64
	readOnly  bool
	// hasHeader: the segment carries an opaque block-0 header (a watermark,
	// in reducible use), framed like a record but not counted in the index.
	hasHeader bool
	header    []byte

	// registered marks this segment findable by the payload cache's Source.
	registered atomic.Bool
	// h is this segment's node in the payload cache, resolved once.
	h atomic.Pointer[tree.Handle[unit]]
}

func Create(path string, codec SegmentCodec, baseIndex uint64, maxSize int64) (*Segment, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	slog.Debug("segment created",
		"codec", codec.Name(),
		"path", path,
		"baseIndex", baseIndex,
		"maxSize", maxSize)
	return &Segment{
		f:         f,
		path:      path,
		codec:     codec,
		baseIndex: baseIndex,
		maxSize:   maxSize,
	}, nil
}

// Open opens an existing segment for read and write. Any torn tail
// (partial frame at end of file) is truncated.
func Open(path string, codec SegmentCodec, baseIndex uint64, maxSize int64) (*Segment, error) {
	return openRW(path, codec, baseIndex, maxSize, false)
}

// OpenHeadered is Open for a segment whose first record is an opaque
// block-0 header (see Segment.hasHeader / WriteHeader).
func OpenHeadered(path string, codec SegmentCodec, baseIndex uint64, maxSize int64) (*Segment, error) {
	return openRW(path, codec, baseIndex, maxSize, true)
}

func openRW(path string, codec SegmentCodec, baseIndex uint64, maxSize int64, hasHeader bool) (*Segment, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	s := &Segment{
		f:         f,
		path:      path,
		codec:     codec,
		baseIndex: baseIndex,
		maxSize:   maxSize,
		hasHeader: hasHeader,
	}
	if err := s.recover(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens an existing segment for reading only. The file is
// scanned to populate the entry index, but a torn tail is left intact
// on disk; Append on the returned Segment returns ErrReadOnly.
func OpenReadOnly(path string, codec SegmentCodec, baseIndex uint64) (*Segment, error) {
	return openRO(path, codec, baseIndex, false)
}

// OpenReadOnlyHeadered is OpenReadOnly for a headered segment.
func OpenReadOnlyHeadered(path string, codec SegmentCodec, baseIndex uint64) (*Segment, error) {
	return openRO(path, codec, baseIndex, true)
}

func openRO(path string, codec SegmentCodec, baseIndex uint64, hasHeader bool) (*Segment, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return nil, err
	}
	s := &Segment{
		f:         f,
		path:      path,
		codec:     codec,
		baseIndex: baseIndex,
		readOnly:  true,
		hasHeader: hasHeader,
	}
	if err := s.recover(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// recover rebuilds the entry index and, for read-write segments, truncates a
// torn tail: every frame before the first torn one is accepted.
func (s *Segment) recover() error {
	start := time.Now()
	if _, err := s.f.Seek(0, 0); err != nil {
		return err
	}
	off := int64(0)
	first := true
	headerEnd := int64(0)
	err := s.codec.ScanFrames(s.f, func(frameOff int64, frameLen int) error {
		if s.hasHeader && first {
			first = false
			headerEnd = frameOff + int64(frameLen)
			off = headerEnd
			return nil
		}
		first = false
		s.offsets = append(s.offsets, frameOff)
		off = frameOff + int64(frameLen)
		s.count++
		return nil
	})
	if err != nil {
		return err
	}
	s.size = off
	if s.hasHeader && headerEnd > 0 {
		payload, _, herr := s.codec.ReadFrame(s.f, 0, headerEnd)
		if herr != nil {
			return herr
		}
		s.header = payload
	}
	fi, err := s.f.Stat()
	if err != nil {
		return err
	}
	tornBytes := int64(0)
	if fi.Size() > off {
		tornBytes = fi.Size() - off
		if s.readOnly {
			slog.Warn("segment open read-only: torn tail ignored",
				"path", s.path, "bytes", tornBytes)
		} else {
			if err := s.f.Truncate(off); err != nil {
				return err
			}
			slog.Warn("segment recover: torn tail truncated",
				"path", s.path, "bytes", tornBytes)
		}
	}
	slog.Debug("segment recovered",
		"codec", s.codec.Name(),
		"path", s.path,
		"baseIndex", s.baseIndex,
		"entries", s.count,
		"size", s.size,
		"tornBytes", tornBytes,
		"readOnly", s.readOnly,
		"duration", time.Since(start))
	return nil
}

func (s *Segment) Append(payload []byte) (offset int64, err error) {
	if s.readOnly {
		return 0, ErrReadOnly
	}
	frame, err := s.codec.Frame(s.baseIndex+s.count, payload)
	if err != nil {
		return 0, err
	}
	if s.maxSize > 0 && s.size+int64(len(frame)) > s.maxSize {
		return 0, ErrFull
	}
	offset = s.size
	n, err := s.f.WriteAt(frame, offset)
	if err != nil {
		return 0, err
	}
	if n != len(frame) {
		return 0, fmt.Errorf("short write: %d/%d", n, len(frame))
	}
	s.size += int64(n)
	s.offsets = append(s.offsets, offset)
	s.count++
	s.extendTail(payload)
	return offset, nil
}

// WriteHeader writes the opaque block-0 header on a fresh segment, before
// any Append. It is framed like a record with index sentinel 0 but never
// counted in the entry index.
func (s *Segment) WriteHeader(payload []byte) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if s.size != 0 || s.count != 0 {
		return fmt.Errorf("WriteHeader: segment not empty (size=%d count=%d)", s.size, s.count)
	}
	frame, err := s.codec.Frame(0, payload)
	if err != nil {
		return err
	}
	n, err := s.f.WriteAt(frame, 0)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return fmt.Errorf("short write: %d/%d", n, len(frame))
	}
	s.size = int64(n)
	s.hasHeader = true
	s.header = append([]byte(nil), payload...)
	return nil
}

// Header returns the opaque block-0 header payload, or nil if the
// segment has none.
func (s *Segment) Header() []byte { return s.header }

// HasHeader reports whether the segment carries a block-0 header.
func (s *Segment) HasHeader() bool { return s.hasHeader }

// ReadAt is the offset-based debug path; ReadIndex is the fast path.
func (s *Segment) ReadAt(offset int64) ([]byte, error) {
	nextOff := s.nextOffsetAfter(offset)
	payload, _, err := s.codec.ReadFrame(s.f, offset, nextOff)
	return payload, err
}

func (s *Segment) ReadIndex(i uint64) ([]byte, error) {
	if i >= s.count {
		return nil, ErrOutOfRange
	}
	if p, ok := s.cachedPayload(i); ok {
		return p, nil
	}
	off := s.offsets[i]
	nextOff := s.size
	if int(i)+1 < len(s.offsets) {
		nextOff = s.offsets[i+1]
	}
	payload, _, err := s.codec.ReadFrame(s.f, off, nextOff)
	return payload, err
}

// nextOffsetAfter is the linear lookup for the offset-based ReadAt path.
func (s *Segment) nextOffsetAfter(off int64) int64 {
	for i, o := range s.offsets {
		if o != off {
			continue
		}
		if i+1 < len(s.offsets) {
			return s.offsets[i+1]
		}
		return s.size
	}
	return -1
}

func (s *Segment) Count() uint64 { return s.count }
func (s *Segment) Sync() error   { return s.f.Sync() }
func (s *Segment) Size() int64   { return s.size }

// Close releases the file handle and the segment's payload block.
func (s *Segment) Close() error {
	s.DropCache()
	return s.f.Close()
}
func (s *Segment) Path() string       { return s.path }
func (s *Segment) ReadOnly() bool     { return s.readOnly }
func (s *Segment) FirstIndex() uint64 { return s.baseIndex }
func (s *Segment) BaseIndex() uint64  { return s.baseIndex }

// LastIndex returns baseIndex + count - 1. The result is only meaningful
// when Count() > 0; callers must check before calling.
func (s *Segment) LastIndex() uint64 { return s.baseIndex + s.count - 1 }
