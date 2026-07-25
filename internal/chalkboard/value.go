// Canonical JSON value for the persistent chalkboard tree.
//
// Lifted from github.com/jack-work/pstate (value.go, MIT, same author) and
// adapted. The one substantive change from the original: pstate canonicalises
// *in place* (it stores only the canonical encoding and throws the caller's
// bytes away). We cannot do that here — chalkboard.json must round-trip
// byte-for-byte and rendered <system-reminder> bodies must not shift under
// users — so a Value keeps both encodings side by side.
//
// ============================ THE INVARIANT ============================
//
//	raw   is EXACTLY the bytes the caller supplied. Every user-visible byte
//	      — MarshalJSON, String, Raw, rendered reminder bodies, the on-disk
//	      chalkboard.json, the RPC ChalkboardResponse — comes from raw.
//	      Nothing else. raw is never rewritten, reordered, or compacted.
//
//	canon is raw compacted with every object's keys sorted recursively.
//	      It exists for ONE purpose: Equal. It is never emitted, never
//	      stored, never rendered.
//
// So {"a":1,"b":2} and {"b":2,"a":1} are Equal (no spurious reminder fires at
// the agent) while each still serialises back as the author wrote it.
//
// Do not "simplify" this by canonicalising raw. That is the single most
// misunderstandable decision in this package, which is why it is shouted here.
//
// =======================================================================

package chalkboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Value is an immutable JSON value: the caller's exact bytes plus a canonical
// form used only for equality. The zero Value is the JSON null literal.
//
// Values are compared with Equal, never with ==: a Value holds slices, so ==
// does not compile, and byte equality is the wrong question anyway.
type Value struct {
	raw   json.RawMessage // exactly as supplied — the only bytes ever emitted
	canon []byte          // compacted, object keys sorted; nil if raw is not valid JSON
}

// NewValue wraps raw bytes, computing the canonical form once. It never fails:
// bytes that are not valid JSON are kept verbatim with no canonical form, and
// such values fall back to byte-exact comparison in Equal.
//
// The caller must not modify raw afterwards. Values are shared freely between
// snapshots (that is the whole point of the persistent tree), so a mutation
// through a retained slice header corrupts every snapshot at once.
func NewValue(raw json.RawMessage) Value {
	canon, err := canonicalJSON(raw)
	if err != nil {
		return Value{raw: raw}
	}
	return Value{raw: raw, canon: canon}
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

// IsJSON reports whether the value's bytes parsed as a single JSON value. When
// false, Equal degrades to a byte-exact comparison.
func (v Value) IsJSON() bool { return v.canon != nil }

// Equal reports semantic JSON equality: whitespace and object key order are
// insignificant, so {"a":1,"b":2} equals {"b":2,"a":1}.
//
// Two values that are not valid JSON are equal only if their bytes match
// exactly. A valid and an invalid value are never equal (their raw bytes
// cannot match, since validity is a function of the bytes), so Equal remains a
// proper equivalence relation across the mix.
//
// Numbers are compared by their literal token, not by numeric value: 1 and 1.0
// are NOT equal, nor are 1e2 and 100. Deliberate — comparing numerically would
// mean choosing a precision policy for arbitrary-precision JSON numbers, and
// the chalkboard has no key where two spellings of one number are meaningfully
// "the same edit". Cheap and lossless beats clever and lossy here.
//
// Duplicate keys within one object follow encoding/json: last occurrence wins,
// so {"a":1,"a":2} equals {"a":2}.
func (v Value) Equal(other Value) bool {
	if v.canon == nil || other.canon == nil {
		return bytes.Equal(v.raw, other.raw)
	}
	return bytes.Equal(v.canon, other.canon)
}

// Decode unmarshals the value into dst.
func (v Value) Decode(dst any) error {
	if err := json.Unmarshal(v.Raw(), dst); err != nil {
		return fmt.Errorf("decode chalkboard value: %w", err)
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
