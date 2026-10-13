package tool

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"nul stripped", "a\x00b", "ab"},
		{"tab newline cr preserved", "a\tb\nc\rd", "a\tb\nc\rd"},
		{"multibyte rune preserved", "café: 日本語 🎉", "café: 日本語 🎉"},
		{"del stripped", "a\x7fb", "ab"},
		{"format char stripped", "a​b", "ab"},
		{"c0 controls stripped", "\x01\x1fok", "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeOutput(tt.in); got != tt.want {
				t.Errorf("sanitizeOutput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		max       int
		truncated bool
	}{
		{"under cap passthrough", "hello world", 100, false},
		{"at cap passthrough", strings.Repeat("a", 50), 50, false},
		{"over cap middle truncation", "HEAD" + strings.Repeat("x", 1000) + "TAIL", 20, true},
		{"disabled by zero cap", strings.Repeat("a", 1000), 0, false},
		{"multibyte rune safe", strings.Repeat("世", 100), 20, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMiddle(tt.input, tt.max)

			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}

			if !tt.truncated {
				if got != tt.input {
					t.Fatalf("under-cap input was altered:\n got %q\nwant %q", got, tt.input)
				}
				if strings.Contains(got, "truncated") {
					t.Fatalf("under-cap input got a truncation marker: %q", got)
				}
				return
			}

			if got == tt.input {
				t.Fatalf("over-cap input was passed through unchanged")
			}

			// Budget preserved: kept runes == max (marker is extra).
			dropped := utf8.RuneCountInString(tt.input) - tt.max
			marker := fmt.Sprintf("[%d characters truncated]", dropped)
			if !strings.Contains(got, marker) {
				t.Fatalf("missing/incorrect drop count, want marker %q in:\n%s", marker, got)
			}
		})
	}
}
func TestTruncateMiddle_PreservesHeadAndTail(t *testing.T) {
	in := "HEAD" + strings.Repeat("x", 1000) + "TAIL"
	got := truncateMiddle(in, 20)

	if !strings.HasPrefix(got, "HEAD") {
		t.Errorf("head not preserved: %q", got)
	}
	if !strings.HasSuffix(got, "TAIL") {
		t.Errorf("tail not preserved: %q", got)
	}
}
func TestMaxOutputChars_EnvOverride(t *testing.T) {
	if got := maxOutputChars(); got != MaxOutputChars {
		t.Fatalf("default cap = %d, want %d", got, MaxOutputChars)
	}

	t.Setenv("FIGARO_BASH_MAX_OUTPUT_CHARS", "500")
	if got := maxOutputChars(); got != 500 {
		t.Fatalf("override cap = %d, want 500", got)
	}

	t.Setenv("FIGARO_BASH_MAX_OUTPUT_CHARS", "garbage")
	if got := maxOutputChars(); got != MaxOutputChars {
		t.Fatalf("invalid override should fall back to %d, got %d", MaxOutputChars, got)
	}
}
