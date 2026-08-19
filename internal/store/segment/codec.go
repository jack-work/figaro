package segment

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

var (
	ErrCorrupt     = errors.New("corrupt entry")
	ErrNotJSON     = errors.New("payload is not valid JSON")
	ErrNotObject   = errors.New("payload must be a JSON object")
	ErrReservedKey = errors.New("payload uses reserved sidecar key (_idx or _hash)")
)

// SegmentCodec abstracts the on-disk representation of one entry.
type SegmentCodec interface {
	// Frame wraps payload into the on-disk byte sequence for one entry,
	// including any framing terminator. idx is the global log index of
	// the entry. Codecs that don't carry it on disk may ignore it.
	Frame(idx uint64, payload []byte) ([]byte, error)

	// ReadFrame reads one entry from r at offset off. nextOff is the byte
	// offset of the following entry, or the segment size for the last
	// entry; codecs that self-describe length may ignore it.
	ReadFrame(r io.ReaderAt, off, nextOff int64) (payload []byte, frameLen int, err error)

	// ScanFrames enumerates every frame in r in order, calling fn with
	// each frame's offset and length. Returns nil on torn tail or clean EOF.
	ScanFrames(r io.Reader, fn func(off int64, frameLen int) error) error

	// Hash returns the integrity token this codec uses for payload, as a
	// short hex string. Binary returns the CRC32; JSONL returns the
	// truncated value-stable SHA-256.
	Hash(payload []byte) (string, error)

	FileExt() string
	Name() string
}

const headerSize = 8

// BinaryCodec is length-prefixed + CRC32, opaque bytes.
type BinaryCodec struct{}

func (BinaryCodec) Name() string    { return "binary" }
func (BinaryCodec) FileExt() string { return ".seg" }

func (BinaryCodec) Hash(payload []byte) (string, error) {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(payload)), nil
}

func (BinaryCodec) Frame(_ uint64, payload []byte) ([]byte, error) {
	buf := make([]byte, headerSize+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(buf[4:8], crc32.ChecksumIEEE(payload))
	copy(buf[headerSize:], payload)
	return buf, nil
}

// ReadFrame trusts the on-disk integrity of the frame; the CRC is
// verified in ScanFrames during segment recovery, not on every read.
// When nextOff is known the read is a single syscall.
func (BinaryCodec) ReadFrame(r io.ReaderAt, off, nextOff int64) ([]byte, int, error) {
	if nextOff > off {
		length := int(nextOff - off)
		if length < headerSize {
			return nil, 0, ErrCorrupt
		}
		buf := make([]byte, length)
		if _, err := r.ReadAt(buf, off); err != nil {
			return nil, 0, err
		}
		n := binary.LittleEndian.Uint32(buf[0:4])
		if int(n)+headerSize != length {
			return nil, 0, ErrCorrupt
		}
		return buf[headerSize:], length, nil
	}
	// Fallback: nextOff unknown. Used by the offset-based debug path.
	var hdr [headerSize]byte
	if _, err := r.ReadAt(hdr[:], off); err != nil {
		return nil, 0, err
	}
	n := binary.LittleEndian.Uint32(hdr[0:4])
	payload := make([]byte, n)
	if n > 0 {
		if _, err := r.ReadAt(payload, off+headerSize); err != nil {
			return nil, 0, err
		}
	}
	return payload, headerSize + int(n), nil
}

func (BinaryCodec) ScanFrames(r io.Reader, fn func(off int64, frameLen int) error) error {
	off := int64(0)
	for {
		var hdr [headerSize]byte
		_, err := io.ReadFull(r, hdr[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
		n := binary.LittleEndian.Uint32(hdr[0:4])
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil
		}
		sum := binary.LittleEndian.Uint32(hdr[4:8])
		if crc32.ChecksumIEEE(payload) != sum {
			return nil
		}
		frameLen := headerSize + int(n)
		if err := fn(off, frameLen); err != nil {
			return err
		}
		off += int64(frameLen)
	}
}

// JSONLCodec stores entries as one flat JSON object per line, with the
// payload's keys at the top level alongside two reserved sidecar keys:
//
//	{"_hash":"<16 hex>","_idx":N,"<payload keys>":...}
//
// `_idx` is the global log index of the entry. `_hash` is the truncated
// value-stable hash of the canonical JSON form of the payload (the
// object with `_idx` and `_hash` removed). Key order on disk is
// alphabetical because `_` sorts before letters, so the sidecar keys
// always lead the line.
//
// Payloads must be JSON objects. Frame returns ErrNotObject for scalars
// or arrays, and ErrReservedKey if the payload contains `_idx` or
// `_hash`. ReadFrame returns ErrCorrupt if the envelope is malformed or
// the hash does not match the payload.
type JSONLCodec struct{}

func (JSONLCodec) Name() string    { return "jsonl" }
func (JSONLCodec) FileExt() string { return ".jsonl" }

func (JSONLCodec) Hash(payload []byte) (string, error) {
	return ValueHash(payload)
}

const (
	sidecarIdx  = "_idx"
	sidecarHash = "_hash"
)

func (JSONLCodec) Frame(idx uint64, payload []byte) ([]byte, error) {
	obj, err := decodeJSONObject(payload)
	if err != nil {
		return nil, err
	}
	if _, exists := obj[sidecarIdx]; exists {
		return nil, ErrReservedKey
	}
	if _, exists := obj[sidecarHash]; exists {
		return nil, ErrReservedKey
	}
	canon, err := marshalCanonical(obj)
	if err != nil {
		return nil, err
	}
	obj[sidecarIdx] = idx
	obj[sidecarHash] = hashCanonical(canon)
	line, err := marshalCanonical(obj)
	if err != nil {
		return nil, err
	}
	return append(line, '\n'), nil
}

// ReadFrame extracts the payload without verifying the stored hash; the
// hash is verified in ScanFrames during recovery. When the line is in
// canonical form (which is what Frame emits) the decode is a slice and
// pointer dance with no JSON parse and no allocations beyond the read
// buffer itself.
func (JSONLCodec) ReadFrame(r io.ReaderAt, off, nextOff int64) ([]byte, int, error) {
	if nextOff <= off {
		return nil, 0, errors.New("jsonl ReadFrame requires nextOff > off")
	}
	length := int(nextOff - off)
	buf := make([]byte, length)
	if _, err := r.ReadAt(buf, off); err != nil {
		return nil, 0, err
	}
	payload, err := decodeJSONLLine(buf, false)
	if err != nil {
		return nil, 0, err
	}
	return payload, length, nil
}

// decodeJSONObject parses b and asserts it decodes to a JSON object.
// Numbers are decoded as json.Number to preserve precision.
func decodeJSONObject(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, ErrNotJSON
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ErrNotObject
	}
	return obj, nil
}

func (JSONLCodec) ScanFrames(r io.Reader, fn func(off int64, frameLen int) error) error {
	reader, ok := r.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReaderSize(r, 64<<10)
	}
	off := int64(0)
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			line = append(line, part...)
			continue
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		frame := part
		if len(line) > 0 {
			line = append(line, part...)
			frame = line
		}
		if _, err := decodeJSONLLine(frame, true); err != nil {
			return nil // torn tail or corruption
		}
		frameLen := len(frame)
		if err := fn(off, frameLen); err != nil {
			return err
		}
		off += int64(frameLen)
		line = line[:0]
	}
}

// decodeJSONLLine returns the payload bytes from a JSONL envelope line
// (with or without a trailing newline). If verify is true, the stored
// `_hash` is recomputed from the payload and a mismatch returns
// ErrCorrupt; this is the recovery-scan path. Otherwise the fast
// canonical-slice path is used when the line matches the on-disk format
// Frame emits, with a parsing fallback for lines that have been hand
// edited (e.g. re-pretty-printed by jq).
//
// In the fast path the returned slice aliases line; in the slow path it
// is a freshly allocated canonical re-marshal.
func decodeJSONLLine(line []byte, verify bool) ([]byte, error) {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	// Fast path for the canonical on-disk form Frame emits: extract the
	// payload as a slice with no JSON parse. When verifying, the stored
	// `_hash` covers exactly this canonical payload, so we hash the slice
	// directly (a single SHA over already-canonical bytes) instead of
	// parsing + re-marshaling (marshalCanonical) to recompute it. That
	// re-canonicalization dominated recovery-scan cost when a segment is
	// opened per operation. Only non-canonical (hand-edited) lines fall to
	// the slow parse path.
	var stored string
	if verify && len(line) >= len(hashPrefix)+hashHexLen && bytes.HasPrefix(line, []byte(hashPrefix)) {
		stored = string(line[len(hashPrefix) : len(hashPrefix)+hashHexLen])
	}
	if payload, ok := fastDecodeCanonicalLine(line); ok {
		if verify && hashCanonical(payload) != stored {
			return nil, ErrCorrupt
		}
		return payload, nil
	}
	return slowDecodeJSONLLine(line, verify)
}

// Fixed prefixes for the canonical envelope. _hash sorts before _idx
// alphabetically, so marshalCanonical always emits them in this order.
const (
	hashPrefix = `{"_hash":"`
	idxPrefix  = `","_idx":`
)

// fastDecodeCanonicalLine extracts the payload bytes from a line in the
// exact canonical form Frame emits, with no JSON parse. It mutates one
// byte of line (the comma between _idx and the first payload key) to
// splice in a leading `{`; the returned slice then aliases the input
// buffer. Callers must pass a buffer they own.
//
// Returns (nil, false) if the line isn't in canonical form. The slow
// path is then responsible for handling it.
func fastDecodeCanonicalLine(line []byte) ([]byte, bool) {
	min := len(hashPrefix) + hashHexLen + len(idxPrefix) + 1 + 1
	if len(line) < min {
		return nil, false
	}
	if !bytes.HasPrefix(line, []byte(hashPrefix)) {
		return nil, false
	}
	p := len(hashPrefix) + hashHexLen
	if !bytes.HasPrefix(line[p:], []byte(idxPrefix)) {
		return nil, false
	}
	p += len(idxPrefix)
	digitsStart := p
	for p < len(line) && line[p] >= '0' && line[p] <= '9' {
		p++
	}
	if p == digitsStart {
		return nil, false
	}
	if line[len(line)-1] != '}' {
		return nil, false
	}
	switch line[p] {
	case '}':
		// Empty payload: line ends right after _idx digits.
		if p != len(line)-1 {
			return nil, false
		}
		return []byte{'{', '}'}, true
	case ',':
		// Splice the comma into an opening brace and return the suffix.
		line[p] = '{'
		return line[p:], true
	default:
		return nil, false
	}
}

// slowDecodeJSONLLine handles lines that aren't in our canonical
// on-disk form (e.g. hand-edited by jq) and the recovery-scan path
// that needs to verify the stored hash.
func slowDecodeJSONLLine(line []byte, verify bool) ([]byte, error) {
	obj, err := decodeJSONObject(line)
	if err != nil {
		return nil, ErrCorrupt
	}
	hashAny, ok := obj[sidecarHash]
	if !ok {
		return nil, ErrCorrupt
	}
	storedHash, ok := hashAny.(string)
	if !ok {
		return nil, ErrCorrupt
	}
	if _, ok := obj[sidecarIdx]; !ok {
		return nil, ErrCorrupt
	}
	delete(obj, sidecarHash)
	delete(obj, sidecarIdx)
	payload, err := marshalCanonical(obj)
	if err != nil {
		return nil, ErrCorrupt
	}
	if verify && hashCanonical(payload) != storedHash {
		return nil, ErrCorrupt
	}
	return payload, nil
}
