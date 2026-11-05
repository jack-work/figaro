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
	got := paths(ix.Query(NewMatcher(), "systools"))
	if len(got) == 0 || got[0] != "system.tools" {
		t.Fatalf("systools ranked %v; want system.tools first", got)
	}
	for _, p := range got {
		if p == "mantra" {
			t.Fatalf("mantra matched systools: %v", got)
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
	if got := ix.Query(NewMatcher(), "sysmodel"); len(got) != 1 {
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
	hits := ix.Query(m, "systools")
	if len(hits) != 1 {
		t.Fatalf("got %v", paths(hits))
	}
	pos := hits[0].KeyPositions(m, "systools")
	var marked strings.Builder
	for _, p := range pos {
		marked.WriteByte(hits[0].Entry.Path[p])
	}
	if marked.String() != "systools" {
		t.Fatalf("marked %q at %v; want the letters of systools", marked.String(), pos)
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
