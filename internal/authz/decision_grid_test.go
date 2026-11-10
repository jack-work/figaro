package authz

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

// The decision grid pins every allow, deny and hidden answer the policies
// gave before grants became verb:object strings. testdata/grid/golden.txt
// was written by this same file run against the { verbs, on } code and the
// { verbs, on } spellings of these fixtures (v0.58.4, jkauthz v0.2.0);
// the fixtures here are their translations. Any row that differs means the
// translation, or the new evaluation, changed a decision.
//
// FIGARO_GRID_WRITE=1 rewrites golden.txt from the code under test.

type gridCombo struct {
	name   string
	self   string
	local  string
	boxes  []string
	troupe string
}

var gridCombos = []gridCombo{
	{name: "spain", self: "user:2001", local: "../spain-policy.toml", troupe: "kelliher-v1.toml"},
	{name: "spain-troupe-test", self: "user:2001", local: "../spain-policy.toml", troupe: "troupe-test.toml"},
	{name: "spain-troupe-wide", self: "user:2001", local: "../spain-policy.toml", troupe: "troupe-wide.toml"},
	{name: "spain-alone", self: "user:2001", local: "../spain-policy.toml"},
	{name: "gluck-default", self: "user:1000", troupe: "kelliher-v1.toml"},
	{name: "gluck-default-boxes", self: "user:1000", boxes: []string{"box0"}},
	{name: "gluck-delegated", self: "user:1000", local: "gluck-delegated.toml", troupe: "kelliher-v1.toml"},
	{name: "gluck-delegated-wide", self: "user:1000", local: "gluck-delegated.toml", troupe: "troupe-wide.toml"},
	{name: "selectors", self: "user:1000", local: "selectors.toml", troupe: "troupe-wide.toml"},
	{name: "selectors-alone", self: "user:1000", local: "selectors.toml"},
}

var gridLints = []string{
	"kelliher-v1.toml", "troupe-test.toml", "troupe-wide.toml",
	"lint-admin-covered.toml", "lint-admin-troupe.toml", "lint-admin-under-star.toml",
	"lint-near-misses.toml", "lint-sandbox-local.toml", "lint-star-troupe.toml",
	"lint-troupe-bind.toml", "lint-unknown-verb.toml", "lint-user-star.toml",
}

var gridSubjects = []string{
	"user:2001", "user:1000", "user:0",
	"user:admin@auth.kelliher.info", "user:guest@auth.kelliher.info", "user:bob@auth.kelliher.info",
	"user:carol@auth.kelliher.info+ops@auth.kelliher.info",
	"sandbox:box0@local:box", "sandbox:chat@local:box", "sandbox:receipt@local:box", "sandbox:stranger@local:box",
	"sandbox:box0", "troupe:kelliher", "node:peer1", "peer1/user:2001",
}

func gridFacts() fakeFacts {
	box0 := jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: "box0@local:box"}
	return fakeFacts{
		owners: map[string]jkauthz.Principal{
			"a-2001":   {Kind: jkauthz.KindUser, ID: "2001"},
			"a-1000":   {Kind: jkauthz.KindUser, ID: "1000"},
			"a-admin":  {Kind: jkauthz.KindUser, ID: "admin@auth.kelliher.info"},
			"a-guest":  {Kind: jkauthz.KindUser, ID: "guest@auth.kelliher.info"},
			"a-box0":   box0,
			"a-child":  box0,
			"a-chat":   {Kind: jkauthz.KindSandbox, ID: "chat@local:box"},
			"a-herald": {Kind: jkauthz.KindUser, ID: "1000"},
		},
		parents: map[string]string{"a-child": "a-box0", "a-grandchild": "a-child"},
		roles:   map[string]string{"@herald": "a-herald"},
		boxed: map[string]string{
			"a-admin": "local", "a-box0": "box0", "a-child": "box0", "a-grandchild": "box0",
			"a-chat": "chat", "a-bwrap": "bwrap", "a-gone": "box0",
		},
		unreadable: map[string]bool{"a-gone": true},
	}
}

func gridResources() []Resource {
	out := []Resource{Daemon}
	for _, id := range []string{"a-2001", "a-1000", "a-admin", "a-guest", "a-box0", "a-child", "a-grandchild", "a-chat", "a-herald", "a-bwrap", "a-gone", "a-unknown"} {
		out = append(out, Aria(id))
	}
	for _, name := range []string{"local", "box0", "chat", "receipt", "bwrap", "other"} {
		out = append(out, Sandbox(name))
	}
	return append(out, Troupe("kelliher"), Troupe("other"))
}

func gridSubject(t *testing.T, text string) jkauthz.Subject {
	t.Helper()
	name, group, _ := strings.Cut(text, "+")
	p, err := jkauthz.ParsePrincipal(name)
	if err != nil {
		t.Fatal(err)
	}
	s := jkauthz.Subject{Principal: p}
	if group != "" {
		s.Groups = []string{group}
	}
	return s
}

func gridDecision(p *Policies, s jkauthz.Subject, verb rpc.Verb, r Resource) byte {
	if p.AllowFigaro(s, "", verb, r).Allowed {
		return 'A'
	}
	if r.Kind == ResourceAria && (verb == rpc.VerbRead || !p.AllowFigaro(s, "", rpc.VerbRead, r).Allowed) {
		return 'H'
	}
	return 'D'
}

func gridRows(t *testing.T, dir string) []string {
	t.Helper()
	var rows []string
	for _, c := range gridCombos {
		self, err := jkauthz.ParsePrincipal(c.self)
		if err != nil {
			t.Fatal(err)
		}
		p := NewPolicies(self, gridFacts())
		p.SetBoxes(c.boxes)
		local := ""
		if c.local != "" {
			local = filepath.Join(dir, c.local)
		}
		if err := p.Reload(local); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.troupe != "" {
			body, err := os.ReadFile(filepath.Join(dir, c.troupe))
			if err != nil {
				t.Fatal(err)
			}
			if err := p.SetTroupe("kelliher", body); err != nil {
				t.Fatalf("%s: troupe %s: %v", c.name, c.troupe, err)
			}
		}
		for _, text := range gridSubjects {
			s := gridSubject(t, text)
			rows = append(rows, fmt.Sprintf("hold %s %s %v", c.name, text, p.HoldsRole(s)))
			for _, r := range gridResources() {
				var line []byte
				for _, v := range rpc.Verbs() {
					line = append(line, gridDecision(p, s, v, r))
				}
				rows = append(rows, fmt.Sprintf("decide %s %s %s %s", c.name, text, strings.ReplaceAll(r.String(), " ", "-"), line))
			}
		}
	}
	for _, name := range gridLints {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		verdict := "accepted"
		if CheckTroupeDocument(body, "kelliher", gridFacts()) != nil {
			verdict = "refused"
		}
		rows = append(rows, fmt.Sprintf("lint %s %s", name, verdict))
	}
	return rows
}

func TestEveryDecisionMatchesTheGridRecordedBeforeGrantsWereStrings(t *testing.T) {
	dir := filepath.Join("testdata", "grid")
	got := gridRows(t, dir)
	golden := filepath.Join(dir, "golden.txt")
	if os.Getenv("FIGARO_GRID_WRITE") == "1" {
		head := "# verbs: " + fmt.Sprint(rpc.Verbs()) + "; A allow, D deny, H hidden\n"
		if err := os.WriteFile(golden, []byte(head+strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")[1:]
	if len(want) != len(got) {
		t.Fatalf("grid has %d rows, golden %d", len(got), len(want))
	}
	diffs := 0
	for i := range want {
		if want[i] != got[i] {
			diffs++
			if diffs <= 20 {
				t.Errorf("row %d\n want %s\n  got %s", i+2, want[i], got[i])
			}
		}
	}
	if diffs > 0 {
		t.Fatalf("%d of %d rows differ", diffs, len(want))
	}
}
