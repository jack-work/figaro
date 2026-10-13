package rpc

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"
)

func TestWithCallerRoundTrip(t *testing.T) {
	t.Run("struct params keep their fields", func(t *testing.T) {
		raw, err := WithCaller(ForkRequest{FigaroID: "target01", AtTurn: 7}, "caller99", nil)
		if err != nil {
			t.Fatalf("WithCaller: %v", err)
		}
		if got := CallerOf(raw); got != "caller99" {
			t.Fatalf("CallerOf = %q, want caller99", got)
		}
		var req ForkRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if req.FigaroID != "target01" || req.AtTurn != 7 {
			t.Fatalf("payload mangled: %+v", req)
		}
	})

	t.Run("nil params still carry the identity", func(t *testing.T) {
		// figaro.context and figaro.form pass nil params.
		raw, err := WithCaller(nil, "caller99", nil)
		if err != nil {
			t.Fatalf("WithCaller(nil): %v", err)
		}
		if got := CallerOf(raw); got != "caller99" {
			t.Fatalf("CallerOf = %q, want caller99", got)
		}
	})

	t.Run("empty caller adds nothing", func(t *testing.T) {
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
		in := SetRequest{Patch: form.Build(form.Snapshot{}, map[string]json.RawMessage{"k": json.RawMessage(`{"deep":[1,2,3]}`)}, nil)}
		raw, err := WithCaller(in, "caller99", nil)
		if err != nil {
			t.Fatalf("WithCaller: %v", err)
		}
		var out SetRequest
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		ent, _ := out.Patch.Entry("k")
		if got := string(ent.New); got != `{"deep":[1,2,3]}` {
			t.Fatalf("value re-encoded: %s", got)
		}
	})
}

// A payload that cannot carry the credential errors rather than dropping it.
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

// The presented id names on-disk directories, so it is validated on decode.
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
