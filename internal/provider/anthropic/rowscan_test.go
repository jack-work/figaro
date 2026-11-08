package anthropic

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// The scanner stands in for json.Unmarshal on the request path; it must
// agree with it on every row the encoders write, and step aside (ok=false)
// rather than guess on anything else.

func refsByUnmarshal(row []byte) ([]blockRef, bool) {
	var m nativeMessage
	if json.Unmarshal(row, &m) != nil {
		return nil, false
	}
	out := make([]blockRef, 0, len(m.Content))
	for _, b := range m.Content {
		out = append(out, blockRef{typ: b.Type, id: b.ID, toolUseID: b.ToolUseID})
	}
	return out, true
}

func randomText(r *rand.Rand) string {
	parts := []string{"plain", `"quoted"`, `back\slash`, "{[brace]}", "<tag>&amp;", "\u2028sep\u2029", "ünï", "tab\tnl\n", `\"`, ",:"}
	s := ""
	for i := r.Intn(5); i >= 0; i-- {
		s += parts[r.Intn(len(parts))]
	}
	return s
}

func randomRow(r *rand.Rand) []byte {
	m := nativeMessage{Role: []string{"user", "assistant", "system"}[r.Intn(3)]}
	for i := r.Intn(6); i >= 0; i-- {
		switch r.Intn(5) {
		case 0:
			m.Content = append(m.Content, nativeBlock{Type: "text", Text: randomText(r)})
		case 1:
			m.Content = append(m.Content, nativeBlock{Type: "tool_use", ID: fmt.Sprintf("toolu_%d%s", r.Intn(50), randomText(r)), Name: "bash",
				Input: map[string]any{"command": randomText(r), "n": r.Intn(9), "nested": []any{randomText(r), nil, true}}})
		case 2:
			m.Content = append(m.Content, nativeBlock{Type: "tool_result", ToolUseID: fmt.Sprintf("toolu_%d", r.Intn(50)), IsError: r.Intn(2) == 0,
				Content: []nativeBlock{{Type: "text", Text: randomText(r)}}})
		case 3:
			m.Content = append(m.Content, nativeBlock{Type: "thinking", Thinking: randomText(r), Signature: "sig"})
		default:
			m.Content = append(m.Content, nativeBlock{Type: "image", Source: map[string]any{"type": "base64", "data": "QUJD"}})
		}
	}
	row, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return row
}

func TestTheRowScannerAgreesWithUnmarshalOnEncodedRows(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		row := randomRow(r)
		want, _ := refsByUnmarshal(row)
		got, ok := scanBlocks(row)
		if !ok {
			t.Fatalf("row %d: the scanner gave up on an encoded row: %s", i, row)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("row %d: scanner %v, unmarshal %v: %s", i, got, want, row)
		}
		var head struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(row, &head)
		if role := rowRole(row); role != head.Role {
			t.Fatalf("row %d: role %q, want %q", i, role, head.Role)
		}
	}
}

func TestTheRowScannerStepsAsideOnWhatItDoesNotWrite(t *testing.T) {
	for _, row := range []string{
		`{"role":"user","content":"a string body"}`,
		`{"role":"user","content":[{"Type":"tool_result","tool_use_id":"x"}]}`,
		`{"role":"user","content":[{"type":5}]}`,
		`{"role":"user","content":[{"type":"text"`,
		`{"role":"user","content":[{"type":"text"},]}`,
		`{"role":"user","con\u0074ent":[]}`,
		`["not","an","object"]`,
		``,
	} {
		if refs, ok := scanBlocks([]byte(row)); ok {
			t.Errorf("%s: scanner answered %v; it must defer to Unmarshal", row, refs)
		}
	}
	refs, ok := scanBlocks([]byte(` { "role" : "user" , "content" : [ { "type" : "tool_result" , "tool_use_id" : "a\u0062" } ] } `))
	if !ok || len(refs) != 1 || refs[0].toolUseID != "ab" {
		t.Errorf("spaced row with an escaped id: %v %v", refs, ok)
	}
}
