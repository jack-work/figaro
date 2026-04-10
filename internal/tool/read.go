package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadRequest is the typed input to the read tool. Use this directly
// from Go code; the Tool interface wrapper handles the JSON map form.
type ReadRequest struct {
	// Path to the file. Relative paths resolve against the tool's Cwd.
	Path string
	// Offset is the 1-indexed starting line. Zero means "from line 1".
	Offset int
	// Limit is the maximum number of lines to read. Zero means "no user
	// limit" — truncation will still apply.
	Limit int
}

// ReadResult bundles the text output shown to the model together with
// structured truncation metadata (if any).
type ReadResult struct {
	Content    string
	Truncation *TruncationResult
}

// Reader is the Go-level interface other programs can depend on.
type Reader interface {
	Read(ctx context.Context, req ReadRequest) (ReadResult, error)
}

// ReadTool implements both Reader and the generic Tool interface.
type ReadTool struct {
	Cwd string
}

// NewReadTool constructs a ReadTool bound to cwd.
func NewReadTool(cwd string) *ReadTool { return &ReadTool{Cwd: cwd} }

func (r *ReadTool) Name() string { return "read" }

func (r *ReadTool) Description() string {
	return fmt.Sprintf(
		"Read the contents of a file. Output is truncated to %d lines or %dKB "+
			"(whichever is hit first). Use offset/limit for large files. "+
			"When you need the full file, continue with offset until complete.",
		MaxOutputLines, MaxOutputBytes/1024,
	)
}

func (r *ReadTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":   map[string]interface{}{"type": "string", "description": "Path to the file to read (relative or absolute)"},
			"offset": map[string]interface{}{"type": "number", "description": "Line number to start reading from (1-indexed)"},
			"limit":  map[string]interface{}{"type": "number", "description": "Maximum number of lines to read"},
		},
		"required": []string{"path"},
	}
}

// Execute is the Tool-interface entry point used by the agent. It
// decodes the JSON args, delegates to Read, and renders the result as
// a string.
func (r *ReadTool) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) (string, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	req := ReadRequest{Path: path}
	if off, ok := args["offset"].(float64); ok && off > 0 {
		req.Offset = int(off)
	}
	if lim, ok := args["limit"].(float64); ok && lim > 0 {
		req.Limit = int(lim)
	}

	res, err := r.Read(ctx, req)
	if err != nil {
		return "", err
	}
	if onOutput != nil {
		onOutput([]byte(res.Content))
	}
	return res.Content, nil
}

// Read is the typed Go API. Other programs can call this directly
// without the JSON-map plumbing.
func (r *ReadTool) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if req.Path == "" {
		return ReadResult{}, fmt.Errorf("path is required")
	}
	path := req.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Cwd, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ReadResult{}, err
	}

	content := string(data)
	allLines := strings.Split(content, "\n")
	totalLines := len(allLines)

	startLine := 0
	if req.Offset > 0 {
		startLine = req.Offset - 1
	}
	if startLine >= totalLines {
		return ReadResult{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", startLine+1, totalLines)
	}

	endLine := totalLines
	userLimited := false
	if req.Limit > 0 {
		endLine = startLine + req.Limit
		if endLine > totalLines {
			endLine = totalLines
		}
		userLimited = true
	}

	selected := strings.Join(allLines[startLine:endLine], "\n")
	selectedLines := endLine - startLine

	trunc := TruncateHead(selected, TruncationOptions{})
	startDisplay := startLine + 1

	// First-line-too-big fallback. The selected slice starts at startLine,
	// so the offending line number in the original file is startDisplay.
	if trunc.FirstLineExceedsLimit {
		lineBytes := len(allLines[startLine])
		output := fmt.Sprintf(
			"[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			startDisplay, FormatSize(lineBytes), FormatSize(MaxOutputBytes),
			startDisplay, req.Path, MaxOutputBytes,
		)
		return ReadResult{Content: output, Truncation: &trunc}, nil
	}

	output := trunc.Content
	switch {
	case trunc.Truncated:
		endDisplay := startDisplay + trunc.OutputLines - 1
		nextOffset := endDisplay + 1
		if trunc.TruncatedBy == TruncatedByLines {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
				startDisplay, endDisplay, totalLines, nextOffset)
		} else {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]",
				startDisplay, endDisplay, totalLines, FormatSize(MaxOutputBytes), nextOffset)
		}
	case userLimited && endLine < totalLines:
		remaining := totalLines - endLine
		nextOffset := endLine + 1
		output += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]",
			remaining, nextOffset)
	case !userLimited && selectedLines > 0 && startLine > 0:
		endDisplay := startDisplay + selectedLines - 1
		output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d.]",
			startDisplay, endDisplay, totalLines)
	}

	return ReadResult{Content: output, Truncation: &trunc}, nil
}
