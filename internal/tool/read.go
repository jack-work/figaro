package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Read struct{ Cwd string }

func (r *Read) Name() string { return "read" }
func (r *Read) Description() string {
	return fmt.Sprintf(
		"Read the contents of a file. Output is truncated to %d lines or %dKB "+
			"(whichever is hit first). Use offset/limit for large files. "+
			"When you need the full file, continue with offset until complete.",
		MaxOutputLines, MaxOutputBytes/1024,
	)
}
func (r *Read) Parameters() interface{} {
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

func (r *Read) Execute(_ context.Context, args map[string]interface{}, onOutput OnOutput) (string, error) {
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

	content := string(data)
	allLines := strings.Split(content, "\n")
	totalLines := len(allLines)

	// Parse offset (1-indexed).
	startLine := 0
	if off, ok := args["offset"].(float64); ok && off > 0 {
		startLine = int(off) - 1
	}
	if startLine >= totalLines {
		return "", fmt.Errorf("offset %d is beyond end of file (%d lines total)", startLine+1, totalLines)
	}

	// Parse limit.
	endLine := totalLines
	userLimited := false
	if lim, ok := args["limit"].(float64); ok && lim > 0 {
		endLine = startLine + int(lim)
		if endLine > totalLines {
			endLine = totalLines
		}
		userLimited = true
	}

	selected := strings.Join(allLines[startLine:endLine], "\n")
	selectedLines := endLine - startLine

	// Truncate if too large (head truncation — keep first N lines/bytes).
	output, truncated := truncateHead(selected)

	startDisplay := startLine + 1 // 1-indexed for display

	if truncated {
		// Count how many lines survived truncation.
		outputLines := strings.Count(output, "\n") + 1
		endDisplay := startDisplay + outputLines - 1
		nextOffset := endDisplay + 1
		output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]",
			startDisplay, endDisplay, totalLines, nextOffset)
	} else if userLimited && endLine < totalLines {
		// User set a limit and there's more.
		remaining := totalLines - endLine
		nextOffset := endLine + 1
		output += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]",
			remaining, nextOffset)
	} else if !userLimited && selectedLines > 0 {
		// No truncation, no user limit — show line count for context.
		endDisplay := startDisplay + selectedLines - 1
		if startLine > 0 {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d.]",
				startDisplay, endDisplay, totalLines)
		}
	}

	if onOutput != nil {
		onOutput([]byte(output))
	}
	return output, nil
}

// truncateHead keeps the first MaxOutputLines / MaxOutputBytes of output.
// Returns the truncated string and whether truncation occurred.
func truncateHead(output string) (string, bool) {
	// Check line limit first.
	lines := strings.Split(output, "\n")
	if len(lines) > MaxOutputLines {
		output = strings.Join(lines[:MaxOutputLines], "\n")
		return output, true
	}

	// Check byte limit.
	if len(output) > MaxOutputBytes {
		output = output[:MaxOutputBytes]
		// Find the last newline to avoid a partial last line.
		if idx := strings.LastIndex(output, "\n"); idx >= 0 {
			output = output[:idx]
		}
		return output, true
	}

	return output, false
}
