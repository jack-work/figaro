package form

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Value is an immutable JSON value: the caller's exact bytes, which are the
// only bytes ever emitted, plus a canonical form used only for equality. The
// zero Value is null.
type Value struct {
	raw json.RawMessage
	c   *canonBox // nil only for the zero Value
	o   *objBox
}

// canonBox is shared by every copy of a Value; sync.Once makes concurrent
// readers safe, since snapshots cross goroutines.
type canonBox struct {
	once  sync.Once
	bytes []byte // nil if raw is not valid JSON
}

type objBox struct {
	once    sync.Once
	members map[string]Value
	isObj   bool
}

// NewValue never fails and never parses; invalid JSON is kept verbatim and
// Equal falls back to byte-exact equality for it.
func NewValue(raw json.RawMessage) Value {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return Value{raw: raw, c: &canonBox{}, o: &objBox{}}
}

func EncodeValue(v any) (Value, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return Value{}, fmt.Errorf("encode JSON value: %w", err)
	}
	return NewValue(data), nil
}

// Raw aliases the Value's storage; treat it as read-only.
func (v Value) Raw() json.RawMessage {
	if len(v.raw) == 0 {
		return json.RawMessage("null")
	}
	return v.raw
}

func (v Value) String() string { return string(v.Raw()) }

// canonical is nil when raw is not exactly one JSON value.
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

func (v Value) IsJSON() bool { return v.canonical() != nil }

// Equal is semantic: whitespace and key order are insignificant.
func (v Value) Equal(other Value) bool {
	if bytes.Equal(v.raw, other.raw) {
		return true
	}
	a, b := v.canonical(), other.canonical()
	if a == nil || b == nil {
		return false
	}
	return bytes.Equal(a, b)
}

func (v Value) Decode(dst any) error {
	if err := json.Unmarshal(v.Raw(), dst); err != nil {
		return fmt.Errorf("decode form value: %w", err)
	}
	return nil
}

func (v Value) MarshalJSON() ([]byte, error) { return v.Raw(), nil }

func (v *Value) UnmarshalJSON(data []byte) error {
	*v = NewValue(append(json.RawMessage(nil), data...))
	return nil
}

// canonicalJSON compacts data with keys sorted recursively; it fails on
// anything but exactly one JSON value.
func canonicalJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
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
