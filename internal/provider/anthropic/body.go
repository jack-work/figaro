package anthropic

import (
	"io"

	"github.com/jack-work/figaro/internal/provider"
)

// bodyFunc is the one encoder both framings run.
func bodyFunc(req nativeRequest) provider.BodyFunc {
	rows := req.Messages
	req.Messages = nil
	return func(w io.Writer) error {
		return provider.WriteJSONWithRows(w, req, "messages", rows)
	}
}

// bodyFuncSeq is bodyFunc over a sequence: the body the send path writes.
//
// The sequence is walked ONCE PER CALL and the call is repeatable, which is
// what req.GetBody needs -- a replayed request re-reads the log rather than
// remembering what it sent, and the read is bounded at the coordinate the
// first attempt saw.
func bodyFuncSeq(req nativeRequest, rows provider.RowSeq) provider.BodyFunc {
	req.Messages = nil
	return func(w io.Writer) error {
		return provider.WriteJSONWithRowSeq(w, req, "messages", rows)
	}
}
