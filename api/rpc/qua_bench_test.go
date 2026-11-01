package rpc

import (
	"encoding/json"
	"strings"
	"testing"
)

// The hub decodes a qua's params once per guard on the way to the agent;
// this is one of those decodes for an ordinary text prompt.
func BenchmarkQuaDecodeText(b *testing.B) {
	raw := []byte(`{"text":"what is the total on this receipt, and which card paid it?","form":{"context":{"datetime":"\"Sunday\""}}}`)
	b.ReportAllocs()
	for b.Loop() {
		var req QuaRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuaDecodeImage(b *testing.B) {
	raw, _ := json.Marshal(QuaRequest{Text: "total?", Images: []QuaImage{{MimeType: "image/jpeg", Data: strings.Repeat("QUJD", 300<<10)}}})
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		var req QuaRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			b.Fatal(err)
		}
	}
}
