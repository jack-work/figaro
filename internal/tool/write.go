package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type Write struct{ Cwd string }

func (w *Write) Name() string        { return "write" }
func (w *Write) Description() string { return "Write content to a file. Creates parent directories." }
func (w *Write) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":    map[string]interface{}{"type": "string", "description": "Path to write to"},
			"content": map[string]interface{}{"type": "string", "description": "Content to write"},
		},
		"required": []string{"path", "content"},
	}
}

func (w *Write) Execute(_ context.Context, args map[string]interface{}, onOutput OnOutput) (string, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.Cwd, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	result := fmt.Sprintf("Wrote %d bytes to %s", len(content), path)
	if onOutput != nil {
		onOutput([]byte(result))
	}
	return result, nil
}
