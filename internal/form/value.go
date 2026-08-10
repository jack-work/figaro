// Canonical JSON value for the persistent form tree.
//
// Lifted from github.com/jack-work/pstate (value.go, MIT, same author) and
// adapted. The one substantive change from the original: pstate canonicalises
// *in place* (it stores only the canonical encoding and throws the caller's
// bytes away). We cannot do that here — the form channel must round-trip
// byte-for-byte and rendered <system-reminder> bodies must not shift under
// users — so a Value keeps both encodings side by side.
//
// ============================ THE INVARIANT ============================
//
//	raw   is EXACTLY the bytes the caller supplied. Every user-visible byte
//	      — MarshalJSON, String, Raw, rendered reminder bodies, the on-disk
//	      the form channel, the RPC FormResponse — comes from raw.
//	      Nothing else. raw is never rewritten, reordered, or compacted.
//
//	canon is raw compacted with every object's keys sorted recursively.
//	      It exists for ONE purpose: Equal. It is never emitted, never
//	      stored, never rendered. It is computed LAZILY, on the first
//	      comparison that actually needs it, and memoised in a box shared
//	      by every copy of the Value — canonicalising a whole board eagerly
//	      at load time cost more than the equality checks ever save (see
//	      the reducer fold regressed 5x before this was lazy).
//
// So {"a":1,"b":2} and {"b":2,"a":1} are Equal (no spurious reminder fires at
// the agent) while each still serialises back as the author wrote it.
//
// Do not "simplify" this by canonicalising raw. That is the single most
// misunderstandable decision in this package, which is why it is shouted here.
//
// =======================================================================

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
//
// Values are compared with Equal, never with ==: a Value holds slices, so ==
// does not compile, and byte equality is the wrong question anyway.
type Value struct {
	raw json.RawMessage // exactly as supplied — the only bytes ever emitted
	c   *canonBox       // memoised canonical form; nil only for the zero Value
}

// canonBox memoises one Value's canonical form. It is heap-allocated once per
// NewValue and shared by every copy of that Value, so the parse happens at most
// once no matter how many snapshots hold the value or how many goroutines
// compare it. sync.Once supplies the happens-before edge that makes concurrent
// readers safe — snapshots are published across goroutines by design.
type canonBox struct {
	once  sync.Once
	bytes []byte // nil if raw is not valid JSON
}

// NewValue wraps raw bytes. It never fails and never parses: bytes that are
// not valid JSON are kept verbatim and, when a comparison eventually needs a
// canonical form and cannot get one, Equal falls back to byte-exact equality.
//
// Empty input becomes the null literal. Empty bytes are not a JSON value and
// encoding/json already emits a nil json.RawMessage as null, so normalising
// here loses nothing and buys an important guarantee: what a Value stores is
// exactly what Raw emits, with no substitution at read time.
//
// The caller must not modify raw afterwards. Values are shared freely between
// snapshots (that is the whole point of the persistent tree), so a mutation
// through a retained slice header corrupts every snapshot at once.
func NewValue(raw json.RawMessage) Value {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return Value{raw: raw, c: &canonBox{}}
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
//
// Identical bytes short-circuit, so the overwhelmingly common cases — a value
// re-set to what it already held, a diff walking two boards that agree on most
// keys — cost one memcmp and never parse anything.
//
// Two values that are not valid JSON are equal only if their bytes match
// exactly. A valid and an invalid value are never equal (their raw bytes
// cannot match, since validity is a function of the bytes), so Equal remains a
// proper equivalence relation across the mix.
//
// Numbers are compared by their literal token, not by numeric value: 1 and 1.0
// are NOT equal, nor are 1e2 and 100. Deliberate — comparing numerically would
// mean choosing a precision policy for arbitrary-precision JSON numbers, and
// the form has no key where two spellings of one number are meaningfully
// "the same edit". Cheap and lossless beats clever and lossy here.
//
// Duplicate keys within one object follow encoding/json: last occurrence wins,
// so {"a":1,"a":2} equals {"a":2}.
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

// MarshalJSON implements json.Marshaler. It emits raw — never canon.
func (v Value) MarshalJSON() ([]byte, error) { return v.Raw(), nil }

// UnmarshalJSON implements json.Unmarshaler, preserving the incoming bytes.
func (v *Value) UnmarshalJSON(data []byte) error {
	*v = NewValue(append(json.RawMessage(nil), data...))
	return nil
}

// canonicalJSON returns data compacted with object keys sorted recursively.
// It fails on bytes that are not exactly one JSON value.
//
// The sort comes from encoding/json's documented behaviour of emitting map
// keys in sorted order; canonicalFormPinned in the tests locks that in so a
// toolchain change cannot silently alter equality semantics.
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
