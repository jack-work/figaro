package rpc

import (
	"encoding/json"
	"testing"
)

func decodedAria(params json.RawMessage) string {
	var a struct {
		ID string `json:"figaro_id"`
	}
	if json.Unmarshal(params, &a) != nil {
		return ""
	}
	return a.ID
}

// WithAria always yields params whose aria a full decode agrees with,
// replacing any aria already there.
func TestWithAriaAddresses(t *testing.T) {
	for _, in := range []string{``, `null`, `{}`, ` { } `, `{"text":"hi"}`,
		`{"figaro_id":"old","text":"hi"}`, `{"text":"{\"figaro_id\":\"x\"}"}`} {
		out, err := WithAria(json.RawMessage(in), "abcd1234")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := decodedAria(out); got != "abcd1234" {
			t.Fatalf("%q -> %s decodes to %q", in, out, got)
		}
		if got := AriaOf(out); got != "abcd1234" {
			t.Fatalf("%q -> %s reads as %q", in, out, got)
		}
	}
	if _, err := WithAria(json.RawMessage(`[1]`), "abcd1234"); err == nil {
		t.Fatal("an array took an address")
	}
}

// The fast path never disagrees with the decode it stands in for: a router
// and a policy reading the same params must see the same aria.
func TestAriaOfAgreesWithDecode(t *testing.T) {
	for _, in := range []string{
		`{"figaro_id":"abcd1234"}`,
		`{"figaro_id":"abcd1234","text":"x"}`,
		`{"figaro_id":"abcd1234" , "text":"x"}`,
		`{"figaro_id":"abcd1234","figaro_id":"ffff0000"}`,
		`{"figaro_id":"abcd1234","FIGARO_ID":"ffff0000"}`,
		`{"figaro_id":"abcd1234","figAro_Id":"ffff0000"}`,
		`{"figaro_id":"abcd1234","x":{"figaro_id":"ffff0000"}}`,
		`{"figaro_id":"ab\u0063d"}`,
		`{"text":"x","figaro_id":"abcd1234"}`,
		`{"figaro_id":7}`,
		`{}`,
	} {
		if got, want := AriaOf(json.RawMessage(in)), decodedAria(json.RawMessage(in)); got != want {
			t.Fatalf("%s: AriaOf %q, decode %q", in, got, want)
		}
	}
}

func FuzzAriaOfAgreesWithDecode(f *testing.F) {
	f.Add(`{"figaro_id":"abcd1234","text":"x"}`)
	f.Add(`{"figaro_id":"a","figaro_id":"b"}`)
	f.Fuzz(func(t *testing.T, in string) {
		if !json.Valid([]byte(in)) {
			t.Skip("the rpc layer delivers only valid params")
		}
		if got, want := AriaOf(json.RawMessage(in)), decodedAria(json.RawMessage(in)); got != want {
			t.Fatalf("%q: AriaOf %q, decode %q", in, got, want)
		}
	})
}
