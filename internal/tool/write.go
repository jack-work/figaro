package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/api/message"
)

// WriteRequest is the typed input to the write tool.
type WriteRequest struct {
	Path    string
	Content string
}

// WriteResult reports how many bytes landed on disk.
type WriteResult struct {
	Path         string // absolute path actually written
	BytesWritten int
}

// Writer is the Go-level interface.
type Writer interface {
	Write(ctx context.Context, req WriteRequest) (WriteResult, error)
}

// WriteTool implements Writer and Tool. Serialized per path.
type WriteTool struct {
	CwdFn func() string
}

// NewWriteTool constructs a WriteTool bound to cwd.
func NewWriteTool(cwd string) *WriteTool { return &WriteTool{CwdFn: staticCwd(cwd)} }

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
	// The resolved path is the only fact the caller did not already have.
	result := res.Path
	if onOutput != nil {
		onOutput([]byte(result))
	}
	return []message.Content{message.TextContent(result)}, nil
}

// Write is the typed Go API.
func (w *WriteTool) Write(ctx context.Context, req WriteRequest) (WriteResult, error) {
	if req.Path == "" {
		return WriteResult{}, fmt.Errorf("path is required")
	}
	path := req.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwdOf(w.CwdFn), path)
	}

	var bytesWritten int
	err := WithFileMutex(path, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
			return err
		}
		bytesWritten = len(req.Content)
		return nil
	})
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Path: path, BytesWritten: bytesWritten}, nil
}
