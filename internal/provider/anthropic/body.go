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
