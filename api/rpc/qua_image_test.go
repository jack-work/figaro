package rpc

import (
	"encoding/json"
	"testing"
)

func TestQuaImagesRoundTripAndOldRequestsStillDecode(t *testing.T) {
	in := QuaRequest{Text: "what is this receipt", Images: []QuaImage{{MimeType: "image/jpeg", Data: "AAAA"}}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out QuaRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 1 || out.Images[0] != in.Images[0] || out.Text != in.Text {
		t.Fatalf("round trip lost the image: %s -> %+v", raw, out)
	}

	var old QuaRequest
	if err := json.Unmarshal([]byte(`{"text":"hi"}`), &old); err != nil || old.Images != nil {
		t.Fatalf("a request from before images: %+v, %v", old, err)
	}
	plain, _ := json.Marshal(QuaRequest{Text: "hi"})
	if string(plain) != `{"text":"hi"}` {
		t.Fatalf("a prompt with no image must encode as before, got %s", plain)
	}
}
