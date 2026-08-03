package rpc

import (
	"encoding/json"
	"testing"
)

// The identity has to survive a round trip through a REAL request struct, a
// nil params (the no-argument methods), and an already-populated object —
// those are the three shapes the two client hops actually send.
func TestWithCallerRoundTrip(t *testing.T) {
	t.Run("struct params keep their fields", func(t *testing.T) {
		raw, err := WithCaller(ForkRequest{FigaroID: "target01", AtTurn: 7}, "caller99", nil)
		if err != nil {
			t.Fatalf("WithCaller: %v", err)
		}
		if got := CallerOf(raw); got != "caller99" {
			t.Fatalf("CallerOf = %q, want caller99", got)
		}
		// The payload must be untouched — the caller field rides ALONGSIDE it.
		var req ForkRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if req.FigaroID != "target01" || req.AtTurn != 7 {
			t.Fatalf("payload mangled: %+v", req)
		}
	})

	t.Run("nil params still carry the identity", func(t *testing.T) {
		// figaro.context and figaro.chalkboard pass nil. If nil could not
		// carry an identity, those methods would be unauthenticatable and the
		// policy seam would have a hole in it.
		raw, err := WithCaller(nil, "caller99", nil)
		if err != nil {
			t.Fatalf("WithCaller(nil): %v", err)
		}
		if got := CallerOf(raw); got != "caller99" {
			t.Fatalf("CallerOf = %q, want caller99", got)
		}
	})

	t.Run("empty caller adds nothing", func(t *testing.T) {
		// A human at a terminal has no aria identity; the wire must look
		// exactly as it did before this change.
		raw, err := WithCaller(InterruptRequest{}, "", nil)
		if err != nil {
			t.Fatalf("WithCaller: %v", err)
		}
		if got := CallerOf(raw); got != "" {
			t.Fatalf("CallerOf = %q, want empty", got)
		}
		if string(raw) != "{}" {
			t.Fatalf("raw = %s, want {}", raw)
		}
	})

	t.Run("nested values are not re-encoded", func(t *testing.T) {
		// Values ride as RawMessage. A lossy re-encode here would corrupt
		// chalkboard patches, which are raw JSON by design.
		in := SetRequest{Patch: ChalkboardPatch{
			Set: map[string]json.RawMessage{"k": json.RawMessage(`{"deep":[1,2,3]}`)},
		}}
		raw, err := WithCaller(in, "caller99", nil)
		if err != nil {
			t.Fatalf("WithCaller: %v", err)
		}
		var out SetRequest
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := string(out.Patch.Set["k"]); got != `{"deep":[1,2,3]}` {
			t.Fatalf("value re-encoded: %s", got)
		}
	})
}

// A scalar or array payload cannot hold a named field. Dropping the credential
// silently is the one response that must never happen, so it errors.
func TestWithCallerRejectsNonObjectParams(t *testing.T) {
	for name, params := range map[string]any{
		"array":  []int{1, 2},
		"string": "hello",
		"number": 42,
	} {
		if _, err := WithCaller(params, "caller99", nil); err == nil {
			t.Fatalf("%s params: want error, got nil", name)
		}
	}
}

func TestCallerOfIsTotal(t *testing.T) {
	// An unreadable credential is an ABSENT one. Deciding whether absence is
	// acceptable belongs to the policy, not the decoder, so this never errors.
	cases := map[string]json.RawMessage{
		"nil":            nil,
		"empty":          json.RawMessage(``),
		"null":           json.RawMessage(`null`),
		"not json":       json.RawMessage(`{`),
		"array":          json.RawMessage(`[1,2]`),
		"absent key":     json.RawMessage(`{"figaro_id":"x"}`),
		"wrong type":     json.RawMessage(`{"x-internal-figaro-id":42}`),
		"invalid aria":   json.RawMessage(`{"x-internal-figaro-id":"has spaces"}`),
		"path traversal": json.RawMessage(`{"x-internal-figaro-id":"../../etc"}`),
	}
	for name, raw := range cases {
		if got := CallerOf(raw); got != "" {
			t.Fatalf("%s: CallerOf = %q, want empty", name, got)
		}
	}
}

// The presented id is validated on the way OUT of the decoder, not trusted.
// It reaches paths that name on-disk aria directories, so a caller must not be
// able to smuggle a traversal or an over-long name through the credential.
func TestCallerOfValidatesShape(t *testing.T) {
	ok := json.RawMessage(`{"x-internal-figaro-id":"a1b2c3d4"}`)
	if got := CallerOf(ok); got != "a1b2c3d4" {
		t.Fatalf("CallerOf = %q, want a1b2c3d4", got)
	}
}

func TestCallerFromEnvValidates(t *testing.T) {
	t.Setenv("FIGARO_ARIA", "  abc123  ")
	if got := CallerFromEnv(); got != "abc123" {
		t.Fatalf("trimmed = %q, want abc123", got)
	}
	t.Setenv("FIGARO_ARIA", "not a valid id")
	if got := CallerFromEnv(); got != "" {
		t.Fatalf("malformed = %q, want empty", got)
	}
	t.Setenv("FIGARO_ARIA", "")
	if got := CallerFromEnv(); got != "" {
		t.Fatalf("unset = %q, want empty", got)
	}
}
