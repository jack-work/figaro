package tool

import "strings"

// LineEnding is either LF or CRLF. Mixed-ending files are detected by
// which sequence appears first and are normalized to LF internally.
type LineEnding string

const (
	LineEndingLF   LineEnding = "\n"
	LineEndingCRLF LineEnding = "\r\n"
)

// DetectLineEnding returns CRLF if the first line break in content is
// "\r\n", otherwise LF. Empty / single-line content reports LF.
func DetectLineEnding(content string) LineEnding {
	lf := strings.Index(content, "\n")
	if lf == -1 {
		return LineEndingLF
	}
	crlf := strings.Index(content, "\r\n")
	if crlf == -1 {
		return LineEndingLF
	}
	if crlf < lf {
		return LineEndingCRLF
	}
	return LineEndingLF
}

// NormalizeToLF converts any CRLF or lone CR to LF. Idempotent.
func NormalizeToLF(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return content
}

// RestoreLineEndings re-encodes an LF-normalized string back to the
// requested line ending.
func RestoreLineEndings(content string, ending LineEnding) string {
	if ending == LineEndingCRLF {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

// UTF-8 BOM (EF BB BF).
const utf8BOM = "\uFEFF"

// StripBOM splits off a leading UTF-8 BOM if present. The returned bom
// string is empty or the BOM itself; text is the content without it.
// Callers should prepend bom back onto content before writing.
func StripBOM(content string) (bom, text string) {
	if strings.HasPrefix(content, utf8BOM) {
		return utf8BOM, content[len(utf8BOM):]
	}
	return "", content
}
