package fuzzy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func snapOf(t testing.TB, kv map[string]any) form.Snapshot {
	t.Helper()
	m := map[string]json.RawMessage{}
	for k, v := range kv {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		m[k] = raw
	}
	return form.FromMap(m)
}

func paths(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Entry.Path)
	}
	return out
}

// The dotted key a user types an abbreviation of comes first, not a key that
// merely contains the same letters scattered.
func TestAKeyAbbreviationRanksTheIntendedPathFirst(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{
		"system.tools":          []string{"bash"},
		"system.xwal.tools":     "x",
		"skills.tools.filePath": "/p",
		"mantra":                "m",
	}))
	got := paths(ix.Query(NewMatcher(), "sys.tools"))
	if len(got) == 0 || got[0] != "system.tools" {
		t.Fatalf("sys.tools ranked %v; want system.tools first", got)
	}
	for _, p := range got {
		if p == "mantra" {
			t.Fatalf("mantra matched sys.tools: %v", got)
		}
	}
}

// A collapsed branch hides nothing from search: every leaf is indexed,
// however deep.
func TestEveryLeafIsIndexedRegardlessOfDepth(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"a.b.c.d.deepkey": 1, "top": 2}))
	if got := paths(ix.Query(NewMatcher(), "deepkey")); len(got) != 1 || got[0] != "a.b.c.d.deepkey" {
		t.Fatalf("deep key not found: %v", got)
	}
}

// Values are matched as text, case-insensitively, and a key match outranks a
// value-only match.
func TestValuesMatchBySubstringAfterKeys(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{
		"system.model":    "claude-opus-5.5",
		"notes.deepseek":  "x",
		"system.provider": "DeepSeek via rou2",
	}))
	hits := ix.Query(NewMatcher(), "deepseek")
	got := paths(hits)
	if len(got) != 2 || got[0] != "notes.deepseek" || got[1] != "system.provider" {
		t.Fatalf("got %v; want the key match then the value match", got)
	}
	if hits[1].OnKey || hits[1].ValueAt != 0 {
		t.Fatalf("value hit misreported: %+v", hits[1])
	}
}

// A JSON string is searched as its text, not its quoted JSON.
func TestAStringValueIsSearchedUnquoted(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"k": "say \"hi\"\nthere"}))
	if got := ix.Query(NewMatcher(), "\"hi\""); len(got) != 1 {
		t.Fatalf("quoted text inside a string not found: %v", paths(got))
	}
	if got := ix.Query(NewMatcher(), "\\n"); len(got) != 0 {
		t.Fatalf("the JSON escape matched as if it were text: %v", paths(got))
	}
}

// Every term must match, each on the key or the value.
func TestAllTermsMustMatch(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{
		"system.model":           "opus",
		"system.thinking_effort": "high",
		"decide.model":           "luna",
	}))
	if got := paths(ix.Query(NewMatcher(), "model opus")); len(got) != 1 || got[0] != "system.model" {
		t.Fatalf("got %v; want only system.model", got)
	}
}

// Long values cost a bounded amount: only the first ValueCap bytes are
// indexed, and the entry says it was cut.
func TestLongValuesAreCapped(t *testing.T) {
	var ix Index
	long := strings.Repeat("a", ValueCap) + "needle"
	ix.Update(snapOf(t, map[string]any{"body": long}))
	e := ix.Entries()[0]
	if !e.ValueCut || len(e.value) != ValueCap {
		t.Fatalf("value not capped: cut=%v len=%d", e.ValueCut, len(e.value))
	}
	if got := ix.Query(NewMatcher(), "needle"); len(got) != 0 {
		t.Fatalf("text past the cap was searched: %v", paths(got))
	}
}

// A delta rebuilds only the touched part of the index; untouched subtrees are
// reused by node identity.
func TestAnUpdateReusesUntouchedSubtrees(t *testing.T) {
	kv := map[string]any{"system.model": "a"}
	for i := 0; i < 200; i++ {
		kv[fmt.Sprintf("skills.s%03d.filePath", i)] = "/p"
	}
	snap := snapOf(t, kv)
	var ix Index
	ix.Update(snap)
	if ix.Rebuilt != 201 {
		t.Fatalf("first build rebuilt %d leaves, want 201", ix.Rebuilt)
	}
	next := snap.Apply(form.Build(snap, map[string]json.RawMessage{"system.model": json.RawMessage(`"b"`)}, nil))
	ix.Update(next)
	if ix.Rebuilt != 1 {
		t.Fatalf("a one-key change rebuilt %d leaves, want 1", ix.Rebuilt)
	}
	if got := ix.Query(NewMatcher(), "sys.model"); len(got) != 1 {
		t.Fatalf("updated key lost: %v", paths(got))
	}
	gone := next.Apply(form.Build(next, nil, []string{"skills.s007.filePath"}))
	ix.Update(gone)
	if n := len(ix.Entries()); n != 200 {
		t.Fatalf("after removing one leaf the index holds %d, want 200", n)
	}
}

func TestAnEmptyQueryMatchesNothing(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"a": 1}))
	if got := ix.Query(NewMatcher(), "   "); got != nil {
		t.Fatalf("blank query returned %v", paths(got))
	}
}

// Highlighting asks for the matched characters only for the hits it draws.
func TestKeyPositionsMarkTheMatchedCharacters(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"system.tools": 1}))
	m := NewMatcher()
	hits := ix.Query(m, "sys.tools")
	if len(hits) != 1 {
		t.Fatalf("got %v", paths(hits))
	}
	pos := hits[0].KeyPositions(m, "sys.tools")
	var marked strings.Builder
	for _, p := range pos {
		marked.WriteByte(hits[0].Entry.Path[p])
	}
	if marked.String() != "sys.tools" {
		t.Fatalf("marked %q at %v; want the letters of sys.tools", marked.String(), pos)
	}
}

// fzf's syntax: an exact term, a prefix, a suffix, an exclusion, and a term
// kept to keys or to values.
func TestTheQuerySyntax(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{
		"system.tools":        "bash",
		"system.model":        "opus",
		"skills.tools.path":   "/s",
		"notes.n1":            "tools are listed here",
		"decide.model":        "luna",
		"system.thinking_tok": "tools",
	}))
	m := NewMatcher()
	cases := map[string][]string{
		"'tools":        {"system.tools", "skills.tools.path", "notes.n1", "system.thinking_tok"},
		"^system":       {"system.tools", "system.model", "system.thinking_tok"},
		"model$":        {"system.model", "decide.model"},
		"^system !opus": {"system.tools", "system.thinking_tok"},
		"k:'tools":      {"system.tools", "skills.tools.path"},
		"v:tools":       {"notes.n1", "system.thinking_tok"},
		"!model":        nil,
	}
	for q, want := range cases {
		got := paths(ix.Query(m, q))
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q matched %v, want %v", q, got, want)
		}
	}
}

// The letters an exact or anchored term matched are the ones marked.
func TestExactTermsMarkTheirSpan(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"system.tools": 1}))
	m := NewMatcher()
	for q, want := range map[string]string{"'tools": "tools", "^sys": "sys", "ols$": "ols"} {
		hits := ix.Query(m, q)
		if len(hits) != 1 {
			t.Fatalf("%q: %v", q, paths(hits))
		}
		var got strings.Builder
		for _, p := range hits[0].KeyPositions(m, q) {
			got.WriteByte("system.tools"[p])
		}
		if got.String() != want {
			t.Errorf("%q marked %q, want %q", q, got.String(), want)
		}
	}
}

// A fuzzy term spread its letters across a long path's keys, so 'libretto'
// matched lime.forks.ibex.forks.brett.forks.otto.ctx ahead of the mantra that
// says libretto. A term without a dot matches inside one key; one with a dot
// spans keys as written. A contiguous match, on a key or in a value, ranks
// ahead of letters spread through a key.
func TestATermMatchesWithinOneKeyAndContiguousRanksFirst(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{
		"arias.lime.forks.ibex.forks.brett.forks.otto.ctx": 1,
		"arias.real.mantra":            "correct the libretto",
		"arias.lab.forks.liberetto.id": "x",
		"system.tools":                 "t",
	}))
	m := NewMatcher()
	got := paths(ix.Query(m, "libretto"))
	if len(got) != 2 || got[0] != "arias.real.mantra" || got[1] != "arias.lab.forks.liberetto.id" {
		t.Fatalf("libretto: %v; want the mantra, then the key that spreads it within itself", got)
	}
	if got := paths(ix.Query(m, "systools")); len(got) != 0 {
		t.Errorf("systools spanned two keys: %v", got)
	}
	for _, q := range []string{"sys.tools", "sys tools"} {
		if got := paths(ix.Query(m, q)); len(got) != 1 || got[0] != "system.tools" {
			t.Errorf("%s: %v; want system.tools", q, got)
		}
	}
	hits := ix.Query(m, "libretto")
	pos := hits[1].KeyPositions(m, "libretto")
	key := []rune(hits[1].Entry.Path)
	var marked []rune
	for _, p := range pos {
		marked = append(marked, key[p])
	}
	if string(marked) != "libretto" {
		t.Errorf("positions mark %q, want the letters inside one key", string(marked))
	}
}

// A key with a non-ASCII rune is matched within itself too, and its letters are
// marked by rune, not by byte.
func TestANonASCIIKeyMatchesWithinItself(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"café.crème.brûlée": 1}))
	m := NewMatcher()
	hits := ix.Query(m, "brulee")
	if len(hits) != 1 {
		t.Fatalf("got %v", paths(hits))
	}
	key := []rune(hits[0].Entry.Path)
	var marked []rune
	for _, p := range hits[0].KeyPositions(m, "brulee") {
		marked = append(marked, key[p])
	}
	if string(marked) != "brûlée" {
		t.Errorf("marked %q, want brûlée", string(marked))
	}
}

// The contiguity check folds case without allocating for ASCII keys, and falls
// back to a full fold for any other key.
func TestContainsLowerFoldsCase(t *testing.T) {
	for _, c := range []struct {
		s, lower string
		want     bool
	}{
		{"KingDom", "king", true},
		{"kin-g", "king", false},
		{"Brûlée", "brûlée", true},
		{"ab", "abc", false},
	} {
		if got := containsLower([]byte(c.s), c.lower); got != c.want {
			t.Errorf("containsLower(%q, %q) = %v, want %v", c.s, c.lower, got, c.want)
		}
	}
}

// Gluck knows an aria by its mantra, and a value matched only as a
// substring, so an abbreviation of the mantra found nothing while the same
// letters typed against a key would have. A fuzzy term matches inside one
// value with fzf's matcher, as it does inside one key.
func TestAFuzzyTermFindsAValueByItsLetters(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{
		"arias.real.mantra":  "Restart delivery after a crash",
		"arias.other.mantra": "tune the fig tree",
		"arias.real.id":      "09e4b786557992e7a84fbff7606463f7",
	}))
	m := NewMatcher()
	for _, q := range []string{"rstrtdlv", "dlvry rstrt", "RSTRT", "crash restart"} {
		if got := paths(ix.Query(m, q)); len(got) != 1 || got[0] != "arias.real.mantra" {
			t.Errorf("%q matched %v, want the mantra", q, got)
		}
	}
	for _, q := range []string{"09e4b786557992e7a84fbff7606463f7", "09e4b7", "09e4b786", "09e4b7865579"} {
		if got := paths(ix.Query(m, q)); len(got) != 1 || got[0] != "arias.real.id" {
			t.Errorf("%q matched %v, want the id", q, got)
		}
	}
}

// A value holding the term whole ranks ahead of one that spreads it, as a
// key does.
func TestAContiguousValueRanksAheadOfASpreadOne(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{
		"a.mantra": "line breaks to tonight",
		"b.mantra": "correct the libretto",
	}))
	hits := ix.Query(NewMatcher(), "libretto")
	if got := paths(hits); len(got) != 2 || got[0] != "b.mantra" {
		t.Fatalf("libretto ranked %v; want the contiguous value first", got)
	}
	if hits[0].Spread || !hits[1].Spread {
		t.Errorf("spread misreported: %v %v", hits[0].Spread, hits[1].Spread)
	}
}

// A term whose letters are scattered through the key but whole in the value
// is a value hit: it ranks as contiguous and only the value is marked.
func TestAWholeValueBeatsAScatteredKey(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"notes.lib_rett_o": "the libretto"}))
	m := NewMatcher()
	hits := ix.Query(m, "libretto")
	if len(hits) != 1 || hits[0].Spread || hits[0].ValueAt != 4 {
		t.Fatalf("got %+v; want a contiguous value hit at 4", hits)
	}
	if pos := hits[0].KeyPositions(m, "libretto"); len(pos) != 0 {
		t.Errorf("the key is marked at %v for a value hit", pos)
	}
}

// The marks drawn on a value are the letters the fuzzy term matched, found
// in the text as shown, whatever its case; an exact term marks every
// occurrence; a term that matched the key marks nothing in the value.
func TestValueMarksAreTheMatchedLetters(t *testing.T) {
	var ix Index
	ix.Update(snapOf(t, map[string]any{"arias.real.mantra": "Restart delivery after a crash"}))
	m := NewMatcher()
	shown := "mantra: Restart delivery after a crash"
	from := len("mantra: ")
	for q, want := range map[string]string{
		"rstrtdlv":     "Rstrtdlv",
		"v:'a":         "aaaa",
		"real rstrt":   "Rstrt",
		"rstrt crash$": "Rstrtcrash",
	} {
		hits := ix.Query(m, q)
		if len(hits) != 1 {
			t.Fatalf("%q: %v", q, paths(hits))
		}
		var got []byte
		for _, p := range hits[0].ValueMarks(m, q, shown, from) {
			got = append(got, shown[p])
		}
		if string(got) != want {
			t.Errorf("%q marked %q, want %q", q, got, want)
		}
	}
}

// Typing narrows: a query that extends the last one searches only the last
// one's hits. Whatever the order of keystrokes, the hits are the ones a fresh
// index finds for the same query.
func TestTypingFindsWhatAFreshQueryFinds(t *testing.T) {
	snap := benchForm(1500)
	m := NewMatcher()
	var typed Index
	typed.Update(snap)
	for _, q := range []string{"sys.tools", "credo deep", "!aria", "!ar sys", "odel$", "^sys", "'cache mark", "k:sys v:rou", "keepalive", "sysmo"} {
		for k := 1; k <= len(q); k++ {
			var fresh Index
			fresh.Update(snap)
			want := strings.Join(paths(fresh.Query(m, q[:k])), ",")
			if got := strings.Join(paths(typed.Query(m, q[:k])), ","); got != want {
				t.Fatalf("typed %q: got %d hits, a fresh index finds %d", q[:k], strings.Count(got, ",")+1, strings.Count(want, ",")+1)
			}
		}
	}
}
