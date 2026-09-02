package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/api/message"
)

// EditRequest is the typed input to the edit tool.
type EditRequest struct {
	Path  string
	Edits []EditOp
}

// EditResult reports what the edit tool did.
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

// EditTool implements Editor and Tool. Serialized per path.
type EditTool struct {
	CwdFn func() string
}

// NewEditTool constructs an EditTool bound to cwd.
func NewEditTool(cwd string) *EditTool { return &EditTool{CwdFn: staticCwd(cwd)} }

func (e *EditTool) Name() string { return "edit" }

func (e *EditTool) Description() string {
	return "Replace one exact region of a file with new text. " +
		"old_text must match a UNIQUE region of the file: if it appears more than " +
		"once, include surrounding lines until it does not. " +
		"To make several changes, issue several edit calls; they may be issued " +
		"together and are applied one at a time, each against the file as it " +
		"stands. If two changes touch the same block or adjacent lines, make them " +
		"one call with a wider old_text instead."
}

// Parameters: EVERY VALUE IS A SCALAR STRING, AND THAT IS THE POINT.
func (e *EditTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to edit (relative or absolute)",
			},
			"old_text": map[string]interface{}{
				"type":        "string",
				"description": "Exact text to find, matched against the file as it currently stands. Must appear exactly once: widen it with surrounding lines if it does not.",
			},
			"new_text": map[string]interface{}{
				"type":        "string",
				"description": "Replacement text. Empty deletes the matched region.",
			},
		},
		"required": []string{"path", "old_text", "new_text"},
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

	// The diff is the result; success and path are already known to the caller.
	msg := res.Diff
	if strings.TrimSpace(msg) == "" {
		msg = res.Path
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
		path = filepath.Join(cwdOf(e.CwdFn), path)
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

// parseEditArgs lifts the arg map into EditRequest.
func parseEditArgs(args map[string]interface{}) (EditRequest, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return EditRequest{}, fmt.Errorf("path is required")
	}
	if old, ok := args["old_text"].(string); ok {
		newText, _ := args["new_text"].(string)
		return EditRequest{Path: path, Edits: []EditOp{{OldText: old, NewText: newText}}}, nil
	}
	rawEdits, ok := args["edits"].([]interface{})
	if !ok {
		return EditRequest{}, fmt.Errorf("old_text is required")
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
