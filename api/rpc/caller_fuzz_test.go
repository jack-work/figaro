package rpc

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// FuzzSanitizeLabel drives the guard that keeps an asserted label from
// impersonating an authenticated aria, and from carrying anything that would
// break a terminal row or forge structure in the model's context.
//
// Property-based rather than example-based because the input is arbitrary
// user-controlled text: an example test can only assert the cases someone
// thought of, and the interesting failures here are the ones nobody thought of.
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

		// 1. NEVER impersonates an aria. This is the security property: the
		//    rendered form of an authenticated caller is "aria <id>", so a
		//    label that survives with that prefix is indistinguishable from
		//    proof: the model would be confidently misinformed.
		if strings.HasPrefix(got, AriaLabelPrefix) {
			t.Fatalf("label kept the reserved prefix: %q -> %q", in, got)
		}

		// 2. No control characters. The label is interpolated into terminal
		//    rows and into model context; a newline could forge a
		//    <system-reminder> block, an escape could rewrite the screen.
		for _, r := range got {
			if unicode.IsControl(r) {
				t.Fatalf("control char %q survived: %q -> %q", r, in, got)
			}
		}

		// 3. Bounded. It rides every message.
		if utf8.RuneCountInString(got) > MaxCallerLabelLen {
			t.Fatalf("label exceeded the cap: %d runes", utf8.RuneCountInString(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 survived: %q", got)
		}

		// 4. Idempotent. Sanitizing happens on the way out AND on the way in;
		//    if a second pass changed anything, those two would disagree about
		//    what is stored versus what is displayed.
		if again := SanitizeLabel(got); again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, got, again)
		}

		// 5. Still valid UTF-8 and trimmed, so it cannot smuggle a partial
		//    rune into JSON or leave ragged whitespace in a dim row.
		if got != strings.TrimSpace(got) {
			t.Fatalf("untrimmed: %q", got)
		}
	})
}

// FuzzWithCallerRoundTrip drives the params injection over arbitrary
// identities. The invariant that matters is that attribution NEVER promotes an
// assertion into a credential, whatever either field contains.
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

		// The payload always survives beside whatever identity was carried.
		var fr ForkRequest
		if err := json.Unmarshal(raw, &fr); err != nil {
			t.Fatalf("payload unreadable: %v (%s)", err, raw)
		}
		if fr.FigaroID != "target" {
			t.Fatalf("payload mangled: %q", fr.FigaroID)
		}

		gotID, gotLabel := CallerOf(raw), LabelOf(raw)

		// An id that comes back is always a VALID aria id: it names on-disk
		// directories, so a traversal must never survive the round trip.
		if gotID != "" {
			if err := ValidateAriaID(gotID); err != nil {
				t.Fatalf("invalid id survived: %q (%v)", gotID, err)
			}
		}

		// THE SECURITY PROPERTY. With no valid credential, the attribution can
		// never render as an aria: no label, however crafted, is promoted.
		attr := Attribution(gotID, gotLabel)
		if gotID == "" && strings.HasPrefix(attr, AriaLabelPrefix) {
			t.Fatalf("label %q was promoted to an aria attribution: %q", label, attr)
		}
		// And SenderFrom, the path the agent actually uses, agrees.
		if from := SenderFrom(raw, nil); from != attr {
			t.Fatalf("SenderFrom disagrees with Attribution: %q vs %q", from, attr)
		}
	})
}
