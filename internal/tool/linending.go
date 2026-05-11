package tool

import "strings"

// LineEnding is LF or CRLF.
type LineEnding string

const (
	LineEndingLF   LineEnding = "\n"
	LineEndingCRLF LineEnding = "\r\n"
)

// DetectLineEnding returns CRLF if the first break is \r\n, else LF.
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

// StripBOM splits off a leading UTF-8 BOM if present.
func StripBOM(content string) (bom, text string) {
	if strings.HasPrefix(content, utf8BOM) {
		return utf8BOM, content[len(utf8BOM):]
	}
	return "", content
}
