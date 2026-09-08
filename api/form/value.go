// Canonical JSON value for the persistent form tree.

package form

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Value is an immutable JSON value: the caller's exact bytes plus a canonical
// form used only for equality. The zero Value is the JSON null literal.
type Value struct {
	raw json.RawMessage // exactly as supplied: the only bytes ever emitted
	c   *canonBox       // memoised canonical form; nil only for the zero Value
	o   *objBox         // memoised object members
}

// canonBox memoises one Value's canonical form. It is heap-allocated once per
// NewValue and shared by every copy of that Value, so the parse happens at most
// once no matter how many snapshots hold the value or how many goroutines
// compare it. sync.Once supplies the happens-before edge that makes concurrent
// readers safe: snapshots are published across goroutines by design.
type canonBox struct {
	once  sync.Once
	bytes []byte // nil if raw is not valid JSON
}

// objBox memoises the members of an object value.
//
// A board is read far more often than it is written, and every read walks it:
// parsing the same subtree on each step made a key lookup cost the whole
// board. The members are parsed once and the child Values are stable, so a
// walk down two levels parses two nodes rather than two subtrees.
type objBox struct {
	once    sync.Once
	members map[string]Value
	isObj   bool
}

// NewValue wraps raw bytes. It never fails and never parses: bytes that are
// not valid JSON are kept verbatim and, when a comparison eventually needs a
// canonical form and cannot get one, Equal falls back to byte-exact equality.
func NewValue(raw json.RawMessage) Value {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return Value{raw: raw, c: &canonBox{}, o: &objBox{}}
}

// EncodeValue marshals v and wraps the result.
func EncodeValue(v any) (Value, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return Value{}, fmt.Errorf("encode JSON value: %w", err)
	}
	return NewValue(data), nil
}

// Raw returns the bytes exactly as supplied. The result aliases the Value's
// storage and must be treated as read-only. A zero Value returns the null
// literal so that the result is always valid JSON.
func (v Value) Raw() json.RawMessage {
	if len(v.raw) == 0 {
		return json.RawMessage("null")
	}
	return v.raw
}

// String returns Raw as a string.
func (v Value) String() string { return string(v.Raw()) }

// canonical returns the memoised canonical form, computing it on first use.
// It returns nil when raw is not exactly one JSON value.
func (v Value) canonical() []byte {
	if v.c == nil {
		return nil
	}
	v.c.once.Do(func() {
		if canon, err := canonicalJSON(v.raw); err == nil {
			v.c.bytes = canon
		}
	})
	return v.c.bytes
}

// IsJSON reports whether the value's bytes parse as a single JSON value. When
// false, Equal degrades to a byte-exact comparison. First call parses.
func (v Value) IsJSON() bool { return v.canonical() != nil }

// Equal reports semantic JSON equality: whitespace and object key order are
// insignificant, so {"a":1,"b":2} equals {"b":2,"a":1}.
func (v Value) Equal(other Value) bool {
	if bytes.Equal(v.raw, other.raw) {
		return true
	}
	a, b := v.canonical(), other.canonical()
	if a == nil || b == nil {
		// At least one has no canonical form, and the raw bytes already
		// differ, so byte-exact comparison has said its piece.
		return false
	}
	return bytes.Equal(a, b)
}

// Decode unmarshals the value into dst.
func (v Value) Decode(dst any) error {
	if err := json.Unmarshal(v.Raw(), dst); err != nil {
		return fmt.Errorf("decode form value: %w", err)
	}
	return nil
}

// MarshalJSON implements json.Marshaler. It emits raw: never canon.
func (v Value) MarshalJSON() ([]byte, error) { return v.Raw(), nil }

// UnmarshalJSON implements json.Unmarshaler, preserving the incoming bytes.
func (v *Value) UnmarshalJSON(data []byte) error {
	*v = NewValue(append(json.RawMessage(nil), data...))
	return nil
}

// canonicalJSON returns data compacted with object keys sorted recursively.
// It fails on bytes that are not exactly one JSON value.
func canonicalJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // keep number literals verbatim: no float round-tripping
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode JSON value: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, err
	}
	canon, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode JSON value: %w", err)
	}
	return canon, nil
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("decode JSON value: multiple values")
	}
	return fmt.Errorf("decode JSON value: trailing data: %w", err)
}

// members reports the value's object members, parsed once.
func (v Value) members() (map[string]Value, bool) {
	if v.o == nil {
		return parseMembers(v.raw)
	}
	v.o.once.Do(func() {
		v.o.members, v.o.isObj = parseMembers(v.raw)
	})
	return v.o.members, v.o.isObj
}

func parseMembers(raw json.RawMessage) (map[string]Value, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	i := 0
	for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
		i++
	}
	if i >= len(raw) || raw[i] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	out := make(map[string]Value, len(m))
	for k, r := range m {
		out[k] = NewValue(r)
	}
	return out, true
}
