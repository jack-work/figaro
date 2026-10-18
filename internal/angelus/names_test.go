package angelus

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/handle"
)

// Two old ids share five hex digits, so they share two words; a new id and a
// form stand alone. Every name a listing prints for them must resolve back to
// exactly the id it was printed for.
func TestEveryPrintedNameResolvesToItsID(t *testing.T) {
	h, _ := resolveHandlers(t)
	o1, o2 := "abcde123", "abcde987"
	long := "5a6b7c8d" + strings.Repeat("e", 24)
	form := "@9876" + strings.Repeat("a", 28)
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{o1, o2, long, form}}

	for _, id := range []string{o1, o2, long, form} {
		short, words := names(h.angelus.Backend, id)
		if short == "" || words == "" {
			t.Fatalf("%s: short %q, words %q", id, short, words)
		}
		for _, ref := range []string{short, words, strings.ToUpper(words), stems(words), id} {
			got, err := h.canonicalID(context.Background(), ref)
			if err != nil || got != id {
				t.Errorf("%q, printed for %s, resolves to %q, %v", ref, id, got, err)
			}
		}
	}
	if _, w := names(h.angelus.Backend, long); strings.Count(w, handle.Separator) != handle.MinWords-1 {
		t.Errorf("a lone id gets two words, got %q", w)
	}
	if _, w := names(h.angelus.Backend, o1); strings.Count(w, handle.Separator) != handle.MaxWords-1 {
		t.Errorf("ids sharing five digits get a third word, got %q", w)
	}
	if s, _ := names(h.angelus.Backend, long); s != long[:handle.HexLen] {
		t.Errorf("a lone id's short form is %d digits, got %q", handle.HexLen, s)
	}
}

func TestSharedTwoWordsAreAmbiguousAndNameTheThreeWordForms(t *testing.T) {
	h, _ := resolveHandlers(t)
	o1, o2 := "abcde123", "abcde987"
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{o1, o2}}
	_, err := h.canonicalID(context.Background(), handle.Words(o1, 2))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), handle.Words(o1, 3)) || !strings.Contains(err.Error(), handle.Words(o2, 3)) {
		t.Fatalf("err = %v, want an ambiguity naming both three-word forms", err)
	}
}

func TestUnknownWordsAreNotFoundRatherThanGuessed(t *testing.T) {
	h, _ := resolveHandlers(t)
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{"abcde123"}}
	ref := handle.Words("12345678", 2)
	if got, err := h.canonicalID(context.Background(), ref); err != nil || got != ref {
		t.Fatalf("canonicalID(%q) = %q, %v; want it returned unresolved", ref, got, err)
	}
}

func stems(words string) string {
	sigil := ""
	if strings.HasPrefix(words, "@") {
		sigil, words = "@", words[1:]
	}
	parts := strings.Split(words, handle.Separator)
	for i, p := range parts {
		parts[i] = p[:handle.StemLen]
	}
	return sigil + strings.Join(parts, handle.Separator)
}

// A partial handle resolves like a partial hex id: unique, or refused with the
// candidates named. The pager's :at sent "affi" as typed and got "not found".
func TestAUniqueWordPrefixResolves(t *testing.T) {
	h, _ := resolveHandlers(t)
	a, b := "5a6b7c8d"+strings.Repeat("e", 24), "9f0e1d2c"+strings.Repeat("f", 24)
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{a, b}}
	full := handle.Words(a, 2)
	for _, ref := range []string{full[:4], full[:len(full)-2]} {
		if got, err := h.canonicalID(context.Background(), ref); err != nil || got != a {
			t.Errorf("canonicalID(%q) = %q, %v; want %s (%s)", ref, got, err, a, full)
		}
	}
	if got, err := h.canonicalID(context.Background(), "zzz"); err != nil || got != "zzz" {
		t.Errorf("an unmatched word prefix comes back unresolved, got %q, %v", got, err)
	}
}

func TestAnAmbiguousWordPrefixNamesTheCandidates(t *testing.T) {
	h, _ := resolveHandlers(t)
	o1, o2 := "abcde123", "abcde987"
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{o1, o2}}
	pre := handle.Words(o1, 2)[:5]
	_, err := h.canonicalID(context.Background(), pre)
	if err == nil || !strings.Contains(err.Error(), handle.Words(o1, 3)) || !strings.Contains(err.Error(), handle.Words(o2, 3)) {
		t.Fatalf("canonicalID(%q) err = %v, want both three-word names", pre, err)
	}
}

// A handle whose first letters are all hex ("deface-...") is tried as a hex
// prefix first, then as words when no id starts that way.
func TestAnAllHexWordPrefixFallsBackToWords(t *testing.T) {
	h, _ := resolveHandlers(t)
	var id, pre string
	for v := uint32(0); ; v += 0x10000 {
		cand := fmt.Sprintf("%08x", v) + strings.Repeat("0", 24)
		w := handle.Words(cand, 2)
		if len(w) >= 4 && strings.Trim(w[:4], "abcdef") == "" {
			id, pre = cand, w[:4]
			break
		}
	}
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{id}}
	if got, err := h.canonicalID(context.Background(), pre); err != nil || got != id {
		t.Fatalf("canonicalID(%q) = %q, %v; want %s (%s)", pre, got, err, id, handle.Words(id, 2))
	}
}
