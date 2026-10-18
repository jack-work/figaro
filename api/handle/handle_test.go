package handle

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// The list IS the encoding: reordering, adding or replacing one word renames
// every aria. This hash pins it. Never update it to make a test pass.
const wordsSHA256 = "c240e34731548d19497f0706039f7fc6776cad29e897c9e4a4df2fcb1bd66c02"

func TestWordListIsFrozen(t *testing.T) {
	sum := sha256.Sum256([]byte(wordsFile))
	if got := hex.EncodeToString(sum[:]); got != wordsSHA256 {
		t.Fatalf("words.txt changed (sha256 %s): every handle ever shown would now name a different aria", got)
	}
	if len(words) != 1<<BitsPer {
		t.Fatalf("%d words, want %d", len(words), 1<<BitsPer)
	}
	stems := map[string]string{}
	for _, w := range words {
		if strings.Trim(w, "abcdefghijklmnopqrstuvwxyz") != "" {
			t.Fatalf("%q is not lowercase letters only", w)
		}
		if prev, ok := stems[w[:StemLen]]; ok {
			t.Fatalf("%q and %q share the stem %q", prev, w, w[:StemLen])
		}
		stems[w[:StemLen]] = w
	}
}

func randomID(t *testing.T) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// Two words spell exactly the first five hex digits, so a handle and the
// short hex form are the same name and resolve through the same prefix lookup.
func TestTwoWordsSpellTheFirstFiveHexDigits(t *testing.T) {
	for i := 0; i < 2000; i++ {
		id := randomID(t)
		w := Words(id, 2)
		r, ok := Parse(w)
		if !ok {
			t.Fatalf("%s: Words gave %q, which does not parse", id, w)
		}
		if r.HexPrefix() != id[:HexLen] {
			t.Fatalf("%s: %q decodes to %q, want %q", id, w, r.HexPrefix(), id[:HexLen])
		}
		if !r.Matches(id) {
			t.Fatalf("%s does not match its own handle %q", id, w)
		}
	}
}

func TestThreeWordsNarrowWhatTwoShare(t *testing.T) {
	a, b := "a5647bfb", "a5647e01"
	if Words(a, 2) != Words(b, 2) {
		t.Fatalf("ids sharing five hex digits must share two words: %q %q", Words(a, 2), Words(b, 2))
	}
	if Words(a, 3) == Words(b, 3) {
		t.Fatalf("a third word must tell them apart: %q", Words(a, 3))
	}
	r, ok := Parse(Words(a, 3))
	if !ok || !r.Matches(a) || r.Matches(b) {
		t.Fatalf("three-word ref %+v: matches a=%v b=%v", r, r.Matches(a), r.Matches(b))
	}
	if r.HexPrefix() != "a5647bf" {
		t.Fatalf("three words narrow the lookup to seven hex digits, got %q", r.HexPrefix())
	}
}

func TestEveryFiveDigitPrefixHasItsOwnHandle(t *testing.T) {
	seen := make(map[string]int, 1<<20)
	for v := 0; v < 1<<20; v++ {
		id := fmt.Sprintf("%05x000", v)
		w := Words(id, 2)
		if prev, ok := seen[w]; ok {
			t.Fatalf("%05x and %05x both spell %q", prev, v, w)
		}
		seen[w] = v
	}
}

func TestStemsAndCaseAreAccepted(t *testing.T) {
	id := "a5647bfbc216b8a09ffea9586393c289"
	full := Words(id, 2)
	parts := strings.Split(full, Separator)
	short := parts[0][:StemLen] + Separator + parts[1][:StemLen]
	for _, s := range []string{full, short, strings.ToUpper(full), parts[0][:5] + Separator + parts[1]} {
		r, ok := Parse(s)
		if !ok || r.String() != full {
			t.Fatalf("%q should read as %q, got %+v ok=%v", s, full, r, ok)
		}
	}
	form := Words("@"+id, 2)
	if !strings.HasPrefix(form, "@") {
		t.Fatalf("a form's handle keeps its sigil: %q", form)
	}
	if r, ok := Parse(form); !ok || r.HexPrefix() != "@a5647" {
		t.Fatalf("form handle %q decodes to %+v", form, r)
	}
}

func TestNotEveryHyphenatedWordIsAHandle(t *testing.T) {
	for _, s := range []string{
		"a5647bfb", "a5647", "", "-", "rhubarb", "rhubarb-", "-rhubarb",
		"el-vigia", "rh-ka", "rhubarb-kangaroo-rhubarb-kangaroo",
		"rhubarbs-kangaroo", "spain2/rhubarb-kangaroo", "rhubarb_kangaroo",
	} {
		if r, ok := Parse(s); ok {
			t.Errorf("%q parsed as a handle: %+v", s, r)
		}
	}
}

// A word prefix is letters and hyphens, at least a stem long. "face" is one
// too: the resolver tries it as hex first and as words when no id starts so.
func TestIsPrefix(t *testing.T) {
	for s, want := range map[string]bool{
		"nur": true, "nursery-li": true, "@syc": true, "affidavit-": true,
		"face": true, "nu": false, "a5647": false, "-nur": false, "nur5": false, "": false, "Nursery": false,
	} {
		if IsPrefix(s) != want {
			t.Errorf("IsPrefix(%q) = %v, want %v", s, !want, want)
		}
	}
}
