package tool

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/internal/message"
)

// ReadRequest is the typed input to the read tool.
type ReadRequest struct {
	Path string

	Offset int

	Limit int
}

// ReadResult is the output with optional truncation metadata.
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
	// ImageLimits bounds an inlined image. The zero value takes
	// DefaultImageLimits; the agent overrides MaxBase64 with the store's
	// segment-derived budget, which is usually the stricter of the two.
	ImageLimits ImageLimits
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
			"when you need to view or analyze an image. A large image is scaled down to fit; "+
			"when that happens the result says so and gives the factor for mapping a coordinate "+
			"on what you see back onto the original.",
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

// Execute decodes args and delegates to Read or returns image content.
func (r *ReadTool) Execute(ctx context.Context, args map[string]interface{}, onOutput OnOutput) ([]message.Content, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}

	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(r.Cwd, absPath)
	}

	if mimeType, ok := detectImageMIME(absPath); ok {
		return r.readImage(absPath, mimeType, onOutput)
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
		return nil, err
	}
	if onOutput != nil {
		onOutput([]byte(res.Content))
	}
	return []message.Content{message.TextContent(res.Content)}, nil
}

// readImage inlines an image file, made to fit the configured ceiling.
//
// The picture is the point, so a too-large image is SCALED, not discarded:
// dropping it would leave the model to answer questions about something it
// cannot see. Only an image that cannot be encoded under the ceiling at any
// size returns text alone — and then the note says so plainly, because a model
// that knows it is blind can ask for a crop, while a model that does not will
// invent what it thinks it saw.
func (r *ReadTool) readImage(absPath, mimeType string, onOutput OnOutput) ([]message.Content, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	name := filepath.Base(absPath)
	fitted, ferr := FitImage(data, mimeType, r.ImageLimits)
	if ferr != nil {
		note := fmt.Sprintf("[Image: %s (%s, %s)]\n[image omitted: %s. Ask for a cropped or smaller copy.]",
			name, mimeType, FormatSize(len(data)), ferr)
		if onOutput != nil {
			onOutput([]byte(note))
		}
		return []message.Content{message.TextContent(note)}, nil
	}

	note := fmt.Sprintf("[Image: %s (%s, %s)]", name, fitted.MimeType, FormatSize(len(data)))
	if fit := fitted.Note(); fit != "" {
		note += "\n" + fit
	}
	if onOutput != nil {
		onOutput([]byte(note))
	}
	return []message.Content{
		message.TextContent(note),
		message.ImageContent(fitted.MimeType, fitted.Data),
	}, nil
}

// detectImageMIME checks if a file is a supported image type.
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

// Read is the typed Go API.
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

	// First-line-too-big fallback.
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
