package tool

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/internal/message"
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
		"Read the contents of a file. For text files, output is truncated to %d lines or %dKB "+
			"(whichever is hit first). Use offset/limit for large files. "+
			"Image files (JPEG, PNG, GIF, WebP) are detected automatically and returned as "+
			"vision-compatible image content blocks — always use this tool instead of cat/bash "+
			"when you need to view or analyze an image.",
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
// decodes the JSON args, delegates to Read, and renders the result.
// For image files, returns an image content block. For text files,
// returns a text content block.
func (r *ReadTool) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) ([]message.Content, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(r.Cwd, absPath)
	}

	// Check if the file is an image — return as image content block.
	if mimeType, ok := detectImageMIME(absPath); ok {
		data, err := os.ReadFile(absPath)
		if err != nil {
			return nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		note := fmt.Sprintf("[Image: %s (%s, %s)]", filepath.Base(absPath), mimeType, FormatSize(len(data)))
		if onOutput != nil {
			onOutput([]byte(note))
		}
		return []message.Content{
			message.TextContent(note),
			message.ImageContent(mimeType, encoded),
		}, nil
	}

	// Text file path — use the existing Read logic.
	req := ReadRequest{Path: path}
	if off, ok := args["offset"].(float64); ok && off > 0 {
		req.Offset = int(off)
	}
	if lim, ok := args["limit"].(float64); ok && lim > 0 {
		req.Limit = int(lim)
	}

	res, err := r.Read(ctx, req)
	if err != nil {
		return nil, err
	}
	if onOutput != nil {
		onOutput([]byte(res.Content))
	}
	return []message.Content{message.TextContent(res.Content)}, nil
}

// detectImageMIME sniffs the file to see if it's an image type
// supported by the Anthropic vision API. Returns the MIME type and
// true if supported, or ("", false) otherwise.
func detectImageMIME(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return "", false
	}

	mimeType := http.DetectContentType(buf[:n])
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return mimeType, true
	default:
		return "", false
	}
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
