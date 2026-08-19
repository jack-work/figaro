package segment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// hashHexLen is the length of the truncated value-stable hash used in
// JSONL envelopes: 16 hex chars = 8 bytes = 64 bits.
const hashHexLen = 16

// ValueHash returns the truncated SHA-256 (16 hex chars) of b's canonical
// JSON form: object keys sorted, no whitespace. JSON-equal inputs hash
// to the same value regardless of key order or formatting.
func ValueHash(b []byte) (string, error) {
	canon, err := canonicalJSON(b)
	if err != nil {
		return "", err
	}
	return hashCanonical(canon), nil
}

// hashCanonical hashes already-canonical JSON bytes. Use this when you
// have already produced canonical form via marshalCanonical and want to
// skip the redundant re-canonicalization in ValueHash.
func hashCanonical(canon []byte) string {
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])[:hashHexLen]
}

func canonicalJSON(b []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return marshalCanonical(v)
}

func marshalCanonical(v any) ([]byte, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []byte
		out = append(out, '{')
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			out = append(out, kb...)
			out = append(out, ':')
			vb, err := marshalCanonical(x[k])
			if err != nil {
				return nil, err
			}
			out = append(out, vb...)
		}
		out = append(out, '}')
		return out, nil
	case []any:
		var out []byte
		out = append(out, '[')
		for i, e := range x {
			if i > 0 {
				out = append(out, ',')
			}
			eb, err := marshalCanonical(e)
			if err != nil {
				return nil, err
			}
			out = append(out, eb...)
		}
		out = append(out, ']')
		return out, nil
	default:
		return json.Marshal(v)
	}
}
