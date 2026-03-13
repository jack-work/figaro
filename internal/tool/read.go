package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type Read struct{ Cwd string }

func (r *Read) Name() string        { return "read" }
func (r *Read) Description() string { return "Read the contents of a file." }
func (r *Read) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string", "description": "Path to the file to read (relative or absolute)"},
		},
		"required": []string{"path"},
	}
}

func (r *Read) Execute(_ context.Context, args map[string]interface{}) (string, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Cwd, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
