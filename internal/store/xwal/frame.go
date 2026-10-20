package xwal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// A channel entry is stored as a JSON object so it round-trips through
// either codec. Legacy channels embed JSON under p. Opaque channels put the
// original payload bytes in base64 under p64 so JSONL canonicalization cannot
// rewrite them. Meta remains a free side-channel. Reducible watermarks are
// stored as the bare state object, not wrapped.
type frameObj struct {
	M   uint64          `json:"m"`
	P   json.RawMessage `json:"p,omitempty"`
	P64 *string         `json:"p64,omitempty"`
	// T is the server timestamp in unix milliseconds, stamped by xwal, never
	// by the caller. Legacy records read back zero.
	T int64           `json:"t,omitempty"`
	X json.RawMessage `json:"x,omitempty"`
	// C is the cursor stamp, present only on MAIN records: the tail of every
	// UNKEYED channel at the moment this record was written.
	C map[string]uint64 `json:"c,omitempty"`
}

type Record struct {
	ChannelLT uint64
	MainLT    uint64
	Payload   []byte
	Meta      []byte
	// TS is zero on records written before timestamps existed.
	TS int64
	// Cursors, main records only: where each unkeyed channel stood when this
	// record was written, so a reader can attribute an unkeyed channel's
	// records to turns without an extra read.
	Cursors map[string]uint64
}

func embedJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	q, _ := json.Marshal(string(b))
	return json.RawMessage(q)
}

func encodeFrame(mainLT uint64, payload, meta []byte, ts int64) []byte {
	return encodeChannelFrame(mainLT, payload, meta, false, ts)
}

func encodeChannelFrame(mainLT uint64, payload, meta []byte, opaque bool, ts int64) []byte {
	return encodeStampedFrame(mainLT, payload, meta, opaque, nil, ts)
}

func encodeStampedFrame(mainLT uint64, payload, meta []byte, opaque bool, cursors map[string]uint64, ts int64) []byte {
	o := frameObj{M: mainLT, C: cursors, T: ts}
	if opaque {
		encoded := base64.StdEncoding.EncodeToString(payload)
		o.P64 = &encoded
	} else {
		o.P = embedJSON(payload)
		if len(o.P) == 0 {
			o.P = json.RawMessage("null")
		}
	}
	o.X = embedJSON(meta)
	b, _ := json.Marshal(o)
	return b
}

// decodeFrame ignores meta.
func decodeFrame(f []byte) (uint64, []byte, error) {
	if m, p, _, _, ok := fastDecodeFrame(f); ok {
		return m, p, nil
	}
	var o frameObj
	if err := json.Unmarshal(f, &o); err != nil {
		return 0, nil, fmt.Errorf("xwal: decode frame: %w", err)
	}
	payload, err := o.payload()
	if err != nil {
		return 0, nil, err
	}
	return o.M, payload, nil
}

func decodeRecord(channelLT uint64, f []byte) (Record, error) {
	return decodeRecordFrom(channelLT, f, false)
}

// isMain selects the slow path: only a main record carries a cursor stamp and
// the fast decode does not look for one.
func decodeRecordFrom(channelLT uint64, f []byte, isMain bool) (Record, error) {
	if m, p, x, ts, ok := fastDecodeFrame(f); ok && !isMain {
		return Record{ChannelLT: channelLT, MainLT: m, Payload: p, Meta: x, TS: ts}, nil
	}
	var o frameObj
	if err := json.Unmarshal(f, &o); err != nil {
		return Record{}, fmt.Errorf("xwal: decode frame: %w", err)
	}
	payload, err := o.payload()
	if err != nil {
		return Record{}, err
	}
	return Record{ChannelLT: channelLT, MainLT: o.M, Payload: payload, Meta: o.X, TS: o.T, Cursors: o.C}, nil
}

func (o frameObj) payload() ([]byte, error) {
	if o.P64 == nil {
		return o.P, nil
	}
	payload, err := base64.StdEncoding.DecodeString(*o.P64)
	if err != nil {
		return nil, fmt.Errorf("xwal: decode opaque payload: %w", err)
	}
	return payload, nil
}

func decodeMainLT(f []byte) (uint64, error) {
	if m, ok := fastDecodeMainLT(f); ok {
		return m, nil
	}
	var o struct {
		M uint64 `json:"m"`
	}
	if err := json.Unmarshal(f, &o); err != nil {
		return 0, fmt.Errorf("xwal: decode frame: %w", err)
	}
	return o.M, nil
}

func fastDecodeMainLT(f []byte) (uint64, bool) {
	const prefix = `{"m":`
	if len(f) <= len(prefix) || string(f[:len(prefix)]) != prefix || !json.Valid(f) {
		return 0, false
	}
	i := len(prefix)
	start := i
	var mainLT uint64
	for i < len(f) && f[i] >= '0' && f[i] <= '9' {
		d := uint64(f[i] - '0')
		if mainLT > (^uint64(0)-d)/10 {
			return 0, false
		}
		mainLT = mainLT*10 + d
		i++
	}
	return mainLT, i > start && i < len(f) && f[i] == ','
}

func fastDecodeFrame(f []byte) (uint64, []byte, []byte, int64, bool) {
	const prefix = `{"m":`
	if len(f) <= len(prefix) || string(f[:len(prefix)]) != prefix || !json.Valid(f) {
		return 0, nil, nil, 0, false
	}
	i := len(prefix)
	var mainLT uint64
	start := i
	for i < len(f) && f[i] >= '0' && f[i] <= '9' {
		d := uint64(f[i] - '0')
		if mainLT > (^uint64(0)-d)/10 {
			return 0, nil, nil, 0, false
		}
		mainLT = mainLT*10 + d
		i++
	}
	if i == start {
		return 0, nil, nil, 0, false
	}
	opaque := false
	switch {
	case i+5 <= len(f) && string(f[i:i+5]) == `,"p":`:
		i += 5
	case i+7 <= len(f) && string(f[i:i+7]) == `,"p64":`:
		i += 7
		opaque = true
	default:
		return 0, nil, nil, 0, false
	}
	end, ok := jsonValueEnd(f, i)
	if !ok {
		return 0, nil, nil, 0, false
	}
	payload := f[i:end]
	if opaque {
		if len(payload) < 2 || payload[0] != '"' || payload[len(payload)-1] != '"' {
			return 0, nil, nil, 0, false
		}
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(payload)-2))
		n, err := base64.StdEncoding.Decode(decoded, payload[1:len(payload)-1])
		if err != nil {
			return 0, nil, nil, 0, false
		}
		payload = decoded[:n]
	}
	if end+1 == len(f) && f[end] == '}' {
		return mainLT, payload, nil, 0, true
	}
	// Optional timestamp FIRST — canonical (alphabetical) order is
	// m, p/p64, t, x: the JSONL codec re-sorts keys on disk, and the
	// encoder declares them in the same order so both byte streams
	// match this one grammar.
	var ts int64
	const tsPrefix = `,"t":`
	if end+len(tsPrefix) < len(f) && string(f[end:end+len(tsPrefix)]) == tsPrefix {
		i = end + len(tsPrefix)
		start = i
		for i < len(f) && f[i] >= '0' && f[i] <= '9' {
			d := int64(f[i] - '0')
			if ts > ((1<<63-1)-d)/10 {
				return 0, nil, nil, 0, false
			}
			ts = ts*10 + d
			i++
		}
		if i == start {
			return 0, nil, nil, 0, false
		}
		if i+1 == len(f) && f[i] == '}' {
			return mainLT, payload, nil, ts, true
		}
		end = i
	}
	// Optional meta: ,"x":<value>, the last field of the fast shape
	// (records with cursors take the slow path via decodeRecordFrom).
	const metaPrefix = `,"x":`
	if end+len(metaPrefix) >= len(f) || string(f[end:end+len(metaPrefix)]) != metaPrefix {
		return 0, nil, nil, 0, false
	}
	i = end + len(metaPrefix)
	end, ok = jsonValueEnd(f, i)
	if !ok || end+1 != len(f) || f[end] != '}' {
		return 0, nil, nil, 0, false
	}
	return mainLT, payload, f[i:end], ts, true
}

// JSONValueEnd is exported so the store's row splitter shares this scanner: a
// JSON grammar implemented twice disagrees with itself on the row that matters.
func JSONValueEnd(b []byte, start int) (int, bool) { return jsonValueEnd(b, start) }

func jsonValueEnd(b []byte, start int) (int, bool) {
	if start >= len(b) {
		return 0, false
	}
	switch b[start] {
	case '"':
		for i := start + 1; i < len(b); i++ {
			switch b[i] {
			case '\\':
				i++
			case '"':
				return i + 1, true
			}
		}
		return 0, false
	case '{', '[':
		depth := 0
		inString := false
		for i := start; i < len(b); i++ {
			if inString {
				switch b[i] {
				case '\\':
					i++
				case '"':
					inString = false
				}
				continue
			}
			switch b[i] {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	default:
		// A bare scalar ends at the first structural byte that can follow it.
		// ']' belongs in that set: it cannot occur INSIDE a number, true,
		// false or null, and without it an array's last scalar element runs
		// off the end of the array.
		for i := start; i < len(b); i++ {
			if b[i] == ',' || b[i] == '}' || b[i] == ']' {
				return i, i > start
			}
		}
		return 0, false
	}
}
