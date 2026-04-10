package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// WriteTool implements both Writer and the generic Tool interface.
// Writes are serialized per absolute path via WithFileMutex so
// concurrent callers never interleave on the same file.
type WriteTool struct {
	Cwd string
}

// NewWriteTool constructs a WriteTool bound to cwd.
func NewWriteTool(cwd string) *WriteTool { return &WriteTool{Cwd: cwd} }

func (w *WriteTool) Name() string        { return "write" }
func (w *WriteTool) Description() string { return "Write content to a file. Creates parent directories." }

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

func (w *WriteTool) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) (string, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	res, err := w.Write(ctx, WriteRequest{Path: path, Content: content})
	if err != nil {
		return "", err
	}
	result := fmt.Sprintf("Wrote %d bytes to %s", res.BytesWritten, res.Path)
	if onOutput != nil {
		onOutput([]byte(result))
	}
	return result, nil
}

// Write is the typed Go API.
func (w *WriteTool) Write(ctx context.Context, req WriteRequest) (WriteResult, error) {
	if req.Path == "" {
		return WriteResult{}, fmt.Errorf("path is required")
	}
	path := req.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.Cwd, path)
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
