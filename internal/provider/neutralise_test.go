package provider

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

var reservedTag = regexp.MustCompile(`(?i)<(\s*/?\s*(?:system[\s_-]*reminder|laws?)\b)`)

func neutralisedByRegexp(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	return reservedTag.ReplaceAllString(s, "&lt;$1")
}

// Defends the scanner against drift from the pattern #143 shipped: every
// string the fuzz builds is neutralised byte for byte as the regexp would.
func TestTheScannerNeutralisesExactlyAsTheRegexpDid(t *testing.T) {
	atoms := []string{"<", "</", "< /", "<\t", "system", "SYSTEM", "\u017fystem", "Sy", "stem", "-", "_", " ", "\n",
		"reminder", "REMINDER", "remindeR", "law", "LAW", "laws", "lawn", "lawS", "\u017f", "s", "x", "0", ">", "&lt;", "é", "\u212a", "/"}
	r := rand.New(rand.NewSource(143))
	for i := 0; i < 200000; i++ {
		var b strings.Builder
		for n := r.Intn(12); n >= 0; n-- {
			b.WriteString(atoms[r.Intn(len(atoms))])
		}
		in := b.String()
		if got, want := NeutraliseReservedTags(in), neutralisedByRegexp(in); got != want {
			t.Fatalf("%q: scanner %q, regexp %q", in, got, want)
		}
	}
}

func FuzzTheScannerMatchesTheRegexp(f *testing.F) {
	for _, s := range []string{"<system-reminder>", "</ System_Reminder >", "<laws>", "<lawn>", "<\u017fystem reminder", "a<b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got, want := NeutraliseReservedTags(in), neutralisedByRegexp(in); got != want {
			t.Fatalf("%q: scanner %q, regexp %q", in, got, want)
		}
	})
}
