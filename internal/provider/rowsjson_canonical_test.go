package provider

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A row written as-is must be byte-identical to what json.Marshal of the
// RawMessage would have written: compacted and HTML-escaped.
func TestACanonicalRowIsWrittenAsMarshalWouldWriteIt(t *testing.T) {
	rows := []string{
		`{"role":"user","content":[{"type":"text","text":"plain"}]}`,
		`{"role":"user","content":[{"type":"text","text":"a \"quote\" and \\ and \n"}]}`,
		`{"a": 1}`,
		"{\"a\":\n1}",
		`{"t":"<b>&"}`,
		"{\"t\":\"\u2028\"}",
		`{"t":"\u003cb\u003e"}`,
		`[1,2,{"k":[true,false,null]}]`,
		`"just a string"`,
	}
	for _, row := range rows {
		want, err := json.Marshal(json.RawMessage(row))
		if err != nil {
			t.Fatal(err)
		}
		if canonicalRow([]byte(row)) && !bytes.Equal([]byte(row), want) {
			t.Errorf("%q is called canonical but Marshal writes %q", row, want)
		}
		var buf bytes.Buffer
		if err := WriteJSONWithRowSeq(&buf, struct {
			M []json.RawMessage `json:"m"`
		}{}, "m", SliceRows([]json.RawMessage{json.RawMessage(row)}, nil)); err != nil {
			t.Fatal(err)
		}
		whole, _ := json.Marshal(struct {
			M []json.RawMessage `json:"m"`
		}{[]json.RawMessage{json.RawMessage(row)}})
		if !bytes.Equal(buf.Bytes(), whole) {
			t.Errorf("%q: spliced %s, Marshal %s", row, buf.Bytes(), whole)
		}
	}
}
