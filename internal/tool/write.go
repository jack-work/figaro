package tool

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/sandbox"
)

type WriteRequest struct {
	Path    string
	Content string
}

type WriteResult struct {
	Path         string
	BytesWritten int
}

type WriteTool struct {
	Box   sandbox.Sandbox
	CwdFn func() string
}

func (w *WriteTool) Name() string { return "write" }

func (w *WriteTool) Description() string {
	return "Write content to a file. Creates parent directories."
}

func (w *WriteTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]interface{}{"type": "string", "description": "Path to write to"},
			"content": map[string]interface{}{"type": "string", "description": "Content to write"},
		},
		"required": []string{"path", "content"},
	}
}

func (w *WriteTool) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) ([]message.Content, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	res, err := w.Write(ctx, WriteRequest{Path: path, Content: content})
	if err != nil {
		return nil, err
	}
	if onOutput != nil {
		onOutput([]byte(res.Path))
	}
	return []message.Content{message.TextContent(res.Path)}, nil
}

func (w *WriteTool) Write(ctx context.Context, req WriteRequest) (WriteResult, error) {
	if req.Path == "" {
		return WriteResult{}, fmt.Errorf("path is required")
	}
	path := resolve(w.CwdFn, req.Path)
	fs := w.Box.FS()
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return WriteResult{}, err
	}
	if err := fs.WriteFile(path, []byte(req.Content), 0o644); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Path: path, BytesWritten: len(req.Content)}, nil
}
