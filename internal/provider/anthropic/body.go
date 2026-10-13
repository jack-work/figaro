package anthropic

import (
	"io"

	"github.com/jack-work/figaro/internal/provider"
)

func bodyFunc(req nativeRequest) provider.BodyFunc {
	rows := req.Messages
	req.Messages = nil
	return func(w io.Writer) error {
		return provider.WriteJSONWithRows(w, req, "messages", rows)
	}
}

// bodyFuncSeq is repeatable for req.GetBody: a retry re-reads the log.
func bodyFuncSeq(req nativeRequest, rows provider.RowSeq) provider.BodyFunc {
	req.Messages = nil
	return func(w io.Writer) error {
		return provider.WriteJSONWithRowSeq(w, req, "messages", rows)
	}
}
