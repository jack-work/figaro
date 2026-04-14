package tool

import (
	"context"

	"github.com/jack-work/figaro/internal/message"
)

// OnOutput is called with streaming output chunks during tool execution.
// The tool calls this as output becomes available. The final complete
// result is still returned from Execute. OnOutput is for live display
// while the tool is running.
//
// If nil is passed to Execute, streaming is disabled and only the
// final result is returned.
type OnOutput func(chunk []byte)

// Tool is the interface that all figaro tools implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() any

	// Execute runs the tool. onOutput receives streaming output chunks
	// as they become available (may be nil to disable streaming).
	// Returns structured content blocks (text, images, etc.).
	Execute(ctx context.Context, args map[string]any, onOutput OnOutput) ([]message.Content, error)
}
