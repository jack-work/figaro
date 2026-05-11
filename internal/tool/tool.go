package tool

import (
	"context"

	"github.com/jack-work/figaro/internal/message"
)

// OnOutput is called with streaming output chunks. nil = no streaming.
type OnOutput func(chunk []byte)

// Tool is the interface that all figaro tools implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() any

	// Execute runs the tool.
	Execute(ctx context.Context, args map[string]any, onOutput OnOutput) ([]message.Content, error)
}
