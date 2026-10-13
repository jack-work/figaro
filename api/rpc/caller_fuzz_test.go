package rpc

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func FuzzSanitizeLabel(f *testing.F) {
	for _, seed := range []string{
		"", " ", "Jack", "aria 76062b18", "aria aria x", "ARIA 1",
		"a\nb", "\x1b[31mred", "\x00", strings.Repeat("z", 500),
		"aria\u00a076", "  aria   spaced  ", "\taria 1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := SanitizeLabel(in)

		// Never impersonates an aria: the security property.
		if strings.HasPrefix(got, AriaLabelPrefix) {
			t.Fatalf("label kept the reserved prefix: %q -> %q", in, got)
		}

		for _, r := range got {
			if unicode.IsControl(r) {
				t.Fatalf("control char %q survived: %q -> %q", r, in, got)
			}
		}

		if utf8.RuneCountInString(got) > MaxCallerLabelLen {
			t.Fatalf("label exceeded the cap: %d runes", utf8.RuneCountInString(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 survived: %q", got)
		}

		// Idempotent: sanitizing happens on the way out and on the way in.
		if again := SanitizeLabel(got); again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, got, again)
		}

		if got != strings.TrimSpace(got) {
			t.Fatalf("untrimmed: %q", got)
		}
	})
}

// Attribution never promotes an assertion into a credential.
func FuzzWithCallerRoundTrip(f *testing.F) {
	f.Add("aria0001", "Jack")
	f.Add("", "aria 76062b18")
	f.Add("", "")
	f.Add("a1b2c3d4", "")
	f.Add("../../etc", "aria aria aria x")
	f.Fuzz(func(t *testing.T, id, label string) {
		raw, err := WithCaller(ForkRequest{FigaroID: "target"}, id, &CallerRef{Label: label})
		if err != nil {
			return // non-object params is the only legal error, unreachable here
		}

		var fr ForkRequest
		if err := json.Unmarshal(raw, &fr); err != nil {
			t.Fatalf("payload unreadable: %v (%s)", err, raw)
		}
		if fr.FigaroID != "target" {
			t.Fatalf("payload mangled: %q", fr.FigaroID)
		}

		gotID, gotLabel := CallerOf(raw), LabelOf(raw)

		if gotID != "" {
			if err := ValidateAriaID(gotID); err != nil {
				t.Fatalf("invalid id survived: %q (%v)", gotID, err)
			}
		}

		if from := SenderFrom(raw, nil); strings.HasPrefix(from, AriaLabelPrefix) {
			t.Fatalf("id %q label %q rendered as an aria: %q", gotID, gotLabel, from)
		}
	})
}
