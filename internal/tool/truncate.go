package tool

import (
	"fmt"
	"strings"
)

// Default truncation limits. These are the limits the LLM sees advertised
// in each tool's description, so keep them in sync if you change them.
const (
	MaxOutputLines = 2000
	MaxOutputBytes = 50 * 1024 // 50KB
)

// TruncationResult describes what a truncation call did to its input.
// All byte counts are UTF-8 byte lengths, not rune counts.
type TruncationResult struct {
	Content     string
	Truncated   bool
	TruncatedBy TruncatedBy
	TotalLines  int
	TotalBytes  int
	OutputLines int
	OutputBytes int
	// LastLinePartial is only set by TruncateTail when the single retained
	// line was longer than MaxBytes and had to be cut mid-line.
	LastLinePartial bool
	// FirstLineExceedsLimit is only set by TruncateHead when line 1 alone
	// is larger than MaxBytes. Callers should surface this explicitly
	// rather than emit an empty or mid-line slice.
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

// TruncateHead keeps the first N complete lines, up to the byte limit.
// Suitable for file reads. Never returns a partial line — if the first
// line alone exceeds MaxBytes, returns empty Content with
// FirstLineExceedsLimit=true so the caller can emit a useful fallback
// message instead of a mid-line slice.
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

	// Does the first line alone blow the byte budget?
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

	// Walk lines accumulating bytes. Stop on either limit.
	var kept []string
	byteCount := 0
	truncatedBy := TruncatedByLines
	for i, line := range lines {
		if i >= opts.MaxLines {
			truncatedBy = TruncatedByLines
			break
		}
		// Every line after the first costs one byte for the joining '\n'.
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

// TruncateTail keeps the last N complete lines, up to the byte limit.
// Suitable for bash output where the end matters (errors, final results).
// If even a single trailing line exceeds MaxBytes, returns the tail of
// that line with LastLinePartial=true.
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
			// If we haven't kept anything yet and this single line is larger
			// than MaxBytes, keep its UTF-8-safe tail.
			if len(kept) == 0 {
				tail := truncateBytesFromEnd(line, opts.MaxBytes)
				kept = append(kept, tail)
				byteCount = len(tail)
				lastLinePartial = true
			}
			break
		}
		// Prepend.
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

// truncateBytesFromEnd keeps the last maxBytes of s, advancing past any
// UTF-8 continuation bytes so the result is valid UTF-8.
func truncateBytesFromEnd(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	// 0x80..0xBF are continuation bytes (10xxxxxx). Advance until we hit
	// a lead byte.
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
