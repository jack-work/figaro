package xwal

import (
	"testing"

	"github.com/jack-work/figaro/figwal/segment"
)

// json.Valid is skipped only for frames a codec has already checked against
// their own checksum on the way in; the binary codec does not check on read,
// so its frames keep it.
func TestOnlyAVerifyingCodecSkipsTheJSONCheck(t *testing.T) {
	if !verifiesReads(segment.JSONLCodec{}) {
		t.Fatal("the JSONL codec checks _hash on every read")
	}
	if verifiesReads(segment.BinaryCodec{}) {
		t.Fatal("the binary codec does not check its CRC on read, so its frames must stay validated")
	}
	broken := []byte(`{"m":7,"p":{"a":"b",}}`)
	if _, _, _, _, ok := fastDecodeFrame(broken, false); ok {
		t.Fatal("an unverified frame that is not JSON took the fast path")
	}
	if _, ok := fastDecodeMainLT(broken, false); ok {
		t.Fatal("an unverified frame that is not JSON gave up its main LT")
	}
}
