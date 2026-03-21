package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Edit struct{ Cwd string }

func (e *Edit) Name() string        { return "edit" }
func (e *Edit) Description() string { return "Edit a file by replacing exact text." }
func (e *Edit) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":     map[string]interface{}{"type": "string", "description": "Path to the file to edit"},
			"old_text": map[string]interface{}{"type": "string", "description": "Exact text to find"},
			"new_text": map[string]interface{}{"type": "string", "description": "Replacement text"},
		},
		"required": []string{"path", "old_text", "new_text"},
	}
}

func (e *Edit) Execute(_ context.Context, args map[string]interface{}, onOutput OnOutput) (string, error) {
	path, _ := args["path"].(string)
	oldText, _ := args["old_text"].(string)
	newText, _ := args["new_text"].(string)
	if path == "" || oldText == "" {
		return "", fmt.Errorf("path and old_text are required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.Cwd, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, oldText)
	if count == 0 {
		return "", fmt.Errorf("old_text not found in %s", path)
	}
	if count > 1 {
		return "", fmt.Errorf("old_text found %d times in %s (must be unique)", count, path)
	}
	result := strings.Replace(content, oldText, newText, 1)
	if err := os.WriteFile(path, []byte(result), 0o644); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Edited %s", path)
	if onOutput != nil {
		onOutput([]byte(msg))
	}
	return msg, nil
}
