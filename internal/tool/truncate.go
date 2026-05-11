package tool

import (
	"fmt"
	"strings"
)

// Default truncation limits (keep in sync with tool descriptions).
const (
	MaxOutputLines = 2000
	MaxOutputBytes = 50 * 1024 // 50KB
)

// TruncationResult describes what truncation did.
type TruncationResult struct {
	Content     string
	Truncated   bool
	TruncatedBy TruncatedBy
	TotalLines  int
	TotalBytes  int
	OutputLines int
	OutputBytes int

	LastLinePartial bool

	FirstLineExceedsLimit bool
	MaxLines              int
	MaxBytes              int
}

// TruncatedBy says which limit was hit. Empty string means not truncated.
type TruncatedBy string

const (
	TruncatedByNone  TruncatedBy = ""
	TruncatedByLines TruncatedBy = "lines"
	TruncatedByBytes TruncatedBy = "bytes"
)

// TruncationOptions overrides the default MaxOutputLines / MaxOutputBytes.
// Zero values mean "use the default".
type TruncationOptions struct {
	MaxLines int
	MaxBytes int
}

func (o TruncationOptions) withDefaults() TruncationOptions {
	if o.MaxLines <= 0 {
		o.MaxLines = MaxOutputLines
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = MaxOutputBytes
	}
	return o
}

// TruncateHead keeps the first N lines up to the byte limit.
func TruncateHead(content string, opts TruncationOptions) TruncationResult {
	opts = opts.withDefaults()
	totalBytes := len(content)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	if totalLines <= opts.MaxLines && totalBytes <= opts.MaxBytes {
		return TruncationResult{
			Content:     content,
			Truncated:   false,
			TruncatedBy: TruncatedByNone,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    opts.MaxLines,
			MaxBytes:    opts.MaxBytes,
		}
	}


	if len(lines[0]) > opts.MaxBytes {
		return TruncationResult{
			Content:               "",
			Truncated:             true,
			TruncatedBy:           TruncatedByBytes,
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			OutputLines:           0,
			OutputBytes:           0,
			FirstLineExceedsLimit: true,
			MaxLines:              opts.MaxLines,
			MaxBytes:              opts.MaxBytes,
		}
	}


	var kept []string
	byteCount := 0
	truncatedBy := TruncatedByLines
	for i, line := range lines {
		if i >= opts.MaxLines {
			truncatedBy = TruncatedByLines
			break
		}

		cost := len(line)
		if i > 0 {
			cost++
		}
		if byteCount+cost > opts.MaxBytes {
			truncatedBy = TruncatedByBytes
			break
		}
		kept = append(kept, line)
		byteCount += cost
	}

	out := strings.Join(kept, "\n")
	return TruncationResult{
		Content:     out,
		Truncated:   true,
		TruncatedBy: truncatedBy,
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(kept),
		OutputBytes: len(out),
		MaxLines:    opts.MaxLines,
		MaxBytes:    opts.MaxBytes,
	}
}

// TruncateTail keeps the last N lines up to the byte limit.
func TruncateTail(content string, opts TruncationOptions) TruncationResult {
	opts = opts.withDefaults()
	totalBytes := len(content)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	if totalLines <= opts.MaxLines && totalBytes <= opts.MaxBytes {
		return TruncationResult{
			Content:     content,
			Truncated:   false,
			TruncatedBy: TruncatedByNone,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    opts.MaxLines,
			MaxBytes:    opts.MaxBytes,
		}
	}

	var kept []string
	byteCount := 0
	truncatedBy := TruncatedByLines
	lastLinePartial := false

	for i := len(lines) - 1; i >= 0 && len(kept) < opts.MaxLines; i-- {
		line := lines[i]
		cost := len(line)
		if len(kept) > 0 {
			cost++ // joining '\n' on the left of the segment we're prepending
		}
		if byteCount+cost > opts.MaxBytes {
			truncatedBy = TruncatedByBytes
			// Single line exceeds limit: keep UTF-8-safe tail.
			if len(kept) == 0 {
				tail := truncateBytesFromEnd(line, opts.MaxBytes)
				kept = append(kept, tail)
				byteCount = len(tail)
				lastLinePartial = true
			}
			break
		}

		kept = append([]string{line}, kept...)
		byteCount += cost
	}

	if len(kept) >= opts.MaxLines && byteCount <= opts.MaxBytes {
		truncatedBy = TruncatedByLines
	}

	out := strings.Join(kept, "\n")
	return TruncationResult{
		Content:         out,
		Truncated:       true,
		TruncatedBy:     truncatedBy,
		TotalLines:      totalLines,
		TotalBytes:      totalBytes,
		OutputLines:     len(kept),
		OutputBytes:     len(out),
		LastLinePartial: lastLinePartial,
		MaxLines:        opts.MaxLines,
		MaxBytes:        opts.MaxBytes,
	}
}

// truncateBytesFromEnd keeps the last maxBytes, UTF-8-safe.
func truncateBytesFromEnd(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	// Skip continuation bytes.
	for start < len(s) && s[start]&0xC0 == 0x80 {
		start++
	}
	return s[start:]
}

// FormatSize renders a byte count as B / KB / MB.
func FormatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}
