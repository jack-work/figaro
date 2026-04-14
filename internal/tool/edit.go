package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/internal/message"
)

// EditRequest is the typed input to the edit tool. A single call can
// carry multiple disjoint replacements; all are matched against the
// same original file content, sorted, checked for overlap, and applied
// in one write.
type EditRequest struct {
	Path  string
	Edits []EditOp
}

// EditResult reports what the edit tool did. Diff is a line-numbered
// unified-ish diff; FirstChangedLine is the 1-indexed new-file line
// where the first change appears.
type EditResult struct {
	Path             string // absolute path actually written
	EditsApplied     int
	Diff             string
	FirstChangedLine int
}

// Editor is the Go-level interface.
type Editor interface {
	Edit(ctx context.Context, req EditRequest) (EditResult, error)
}

// EditTool implements both Editor and the generic Tool interface.
// Edits are serialized per absolute path via WithFileMutex.
type EditTool struct {
	Cwd string
}

// NewEditTool constructs an EditTool bound to cwd.
func NewEditTool(cwd string) *EditTool { return &EditTool{Cwd: cwd} }

func (e *EditTool) Name() string { return "edit" }

func (e *EditTool) Description() string {
	return "Edit a single file using one or more exact-text replacements. " +
		"Every edits[].old_text must match a unique, non-overlapping region of " +
		"the original file. All edits are matched against the original content, " +
		"not incrementally. If two changes affect the same block or nearby lines, " +
		"merge them into one edit instead of emitting overlapping edits."
}

func (e *EditTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to edit (relative or absolute)",
			},
			"edits": map[string]interface{}{
				"type":        "array",
				"description": "One or more targeted replacements. Each entry is matched against the original file.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"old_text": map[string]interface{}{
							"type":        "string",
							"description": "Exact text to find. Must be unique in the original file and must not overlap with other edits.",
						},
						"new_text": map[string]interface{}{
							"type":        "string",
							"description": "Replacement text.",
						},
					},
					"required": []string{"old_text", "new_text"},
				},
			},
		},
		"required": []string{"path", "edits"},
	}
}

func (e *EditTool) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) ([]message.Content, error) {
	req, err := parseEditArgs(args)
	if err != nil {
		return nil, err
	}

	res, err := e.Edit(ctx, req)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Successfully applied %d edit(s) to %s", res.EditsApplied, res.Path)
	if res.Diff != "" {
		msg += "\n\n" + res.Diff
	}
	if onOutput != nil {
		onOutput([]byte(msg))
	}
	return []message.Content{message.TextContent(msg)}, nil
}

// Edit is the typed Go API.
func (e *EditTool) Edit(ctx context.Context, req EditRequest) (EditResult, error) {
	if req.Path == "" {
		return EditResult{}, fmt.Errorf("path is required")
	}
	if len(req.Edits) == 0 {
		return EditResult{}, fmt.Errorf("edits must contain at least one replacement")
	}

	path := req.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.Cwd, path)
	}

	var result EditResult
	err := WithFileMutex(path, func() error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rawContent := string(raw)

		bom, stripped := StripBOM(rawContent)
		ending := DetectLineEnding(stripped)
		normalized := NormalizeToLF(stripped)

		applied, err := applyEditsToNormalized(normalized, req.Edits, req.Path)
		if err != nil {
			return err
		}

		final := bom + RestoreLineEndings(applied.newContent, ending)
		if err := os.WriteFile(path, []byte(final), 0o644); err != nil {
			return err
		}

		diff := GenerateDiff(applied.baseContent, applied.newContent, 4)
		result = EditResult{
			Path:             path,
			EditsApplied:     len(req.Edits),
			Diff:             diff.Diff,
			FirstChangedLine: diff.FirstChangedLine,
		}
		return nil
	})
	if err != nil {
		return EditResult{}, err
	}
	return result, nil
}

// TODO: figure out a better way to parse arguments here.
// parseEditArgs lifts the JSON-map arg shape used by the Tool interface
// into a typed EditRequest. Expects:
//
//	{ "path": "...", "edits": [ { "old_text": "...", "new_text": "..." }, ... ] }
func parseEditArgs(args map[string]interface{}) (EditRequest, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return EditRequest{}, fmt.Errorf("path is required")
	}
	rawEdits, ok := args["edits"].([]interface{})
	if !ok {
		return EditRequest{}, fmt.Errorf("edits must be an array")
	}
	if len(rawEdits) == 0 {
		return EditRequest{}, fmt.Errorf("edits must contain at least one replacement")
	}
	edits := make([]EditOp, 0, len(rawEdits))
	for i, item := range rawEdits {
		m, ok := item.(map[string]interface{})
		if !ok {
			return EditRequest{}, fmt.Errorf("edits[%d] must be an object", i)
		}
		oldText, _ := m["old_text"].(string)
		newText, _ := m["new_text"].(string)
		edits = append(edits, EditOp{OldText: oldText, NewText: newText})
	}
	return EditRequest{Path: path, Edits: edits}, nil
}
