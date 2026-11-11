package authz

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

func gridPolicies(t *testing.T, dir string, c gridCombo, f Facts) *Policies {
	t.Helper()
	self, err := jkauthz.ParsePrincipal(c.self)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPolicies(self, f)
	p.SetBoxes(c.boxes)
	local := c.local
	if local != "" && !filepath.IsAbs(local) {
		local = filepath.Join(dir, local)
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
	return p
}

func gridDecision(p *Policies, s jkauthz.Subject, verb rpc.Verb, r Resource) byte {
	return gridClaimDecision(p, s, "", verb, r)
}

func gridClaimDecision(p *Policies, s jkauthz.Subject, figaro string, verb rpc.Verb, r Resource) byte {
	if p.AllowFigaro(s, figaro, verb, r).Allowed {
		return 'A'
	}
	if r.Kind == ResourceAria && (verb == rpc.VerbRead || !p.AllowFigaro(s, figaro, rpc.VerbRead, r).Allowed) {
		return 'H'
	}
	return 'D'
}

func gridRows(t *testing.T, dir string) []string {
	t.Helper()
	var rows []string
	for _, c := range gridCombos {
		p := gridPolicies(t, dir, c, gridFacts())
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
	return append(rows, gridClaimRows(t, dir)...)
}

// Claim rows are new with figaro grants: what a call naming a figaro may do,
// the caller's grants intersected with the figaro's. They were written by
// the code that introduced them; the rows above them are the old answers.
type gridFigaroFacts struct {
	fakeFacts
	chains map[string][]string
	own    map[string]string
}

func (f gridFigaroFacts) Ancestry(id string) ([]string, bool) {
	c, ok := f.chains[id]
	return c, ok
}

func (f gridFigaroFacts) OwnGrants(id string) (json.RawMessage, bool, error) {
	raw, ok := f.own[id]
	return json.RawMessage(raw), ok, nil
}

var gridClaimed = []string{"a-box0", "a-child", "a-grandchild", "a-2001", "a-guest"}

func gridClaimRows(t *testing.T, dir string) []string {
	f := gridFigaroFacts{
		fakeFacts: gridFacts(),
		chains: map[string][]string{
			"a-box0": {"a-box0"}, "a-child": {"a-box0", "a-child"}, "a-grandchild": {"a-box0", "a-child", "a-grandchild"},
			"a-2001": {"a-2001"}, "a-guest": {"a-guest"},
		},
		own: map[string]string{
			"a-box0":       `["read:*", "listen:*", "send:lineage:self", "kill:aria:self", "use:sandbox:self", "egress:*"]`,
			"a-grandchild": `["*:*", "create:*"]`,
			"a-guest":      `[]`,
		},
	}
	combos := []gridCombo{
		{name: "spain", self: "user:2001", local: "../spain-policy.toml", troupe: "kelliher-v1.toml"},
		{name: "spain-ceiling", self: "user:2001", local: "spain-ceiling.toml"},
		{name: "gluck-default-boxes", self: "user:1000", boxes: []string{"box0"}},
		{name: "selectors", self: "user:1000", local: "selectors.toml", troupe: "troupe-wide.toml"},
	}
	subjects := []string{"user:2001", "user:1000", "user:admin@auth.kelliher.info", "user:bob@auth.kelliher.info", "sandbox:box0@local:box"}
	var rows []string
	for _, c := range combos {
		p := gridPolicies(t, dir, c, f)
		for _, text := range subjects {
			s := gridSubject(t, text)
			for _, figaro := range gridClaimed {
				for _, r := range gridResources() {
					var line []byte
					for _, v := range rpc.Verbs() {
						line = append(line, gridClaimDecision(p, s, figaro, v, r))
					}
					rows = append(rows, fmt.Sprintf("claim %s %s %s %s %s", c.name, text, figaro, strings.ReplaceAll(r.String(), " ", "-"), line))
				}
			}
		}
	}
	return rows
}

func gridGolden(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "grid", "golden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")[1:]
}

func gridDiff(want, got []string) []string {
	if len(want) != len(got) {
		return []string{fmt.Sprintf("grid has %d rows, golden %d", len(got), len(want))}
	}
	var diffs []string
	for i := range want {
		if want[i] != got[i] {
			diffs = append(diffs, fmt.Sprintf("row %d\n want %s\n  got %s", i+2, want[i], got[i]))
		}
	}
	return diffs
}

func checkGrid(t *testing.T, dir string) {
	t.Helper()
	diffs := gridDiff(gridGolden(t), gridRows(t, dir))
	for i, d := range diffs {
		if i == 20 {
			break
		}
		t.Error(d)
	}
	if len(diffs) > 0 {
		t.Fatalf("%d rows differ", len(diffs))
	}
}

func TestEveryDecisionMatchesTheGridRecordedBeforeGrantsWereStrings(t *testing.T) {
	dir := filepath.Join("testdata", "grid")
	if os.Getenv("FIGARO_GRID_WRITE") == "1" {
		head := "# verbs: " + fmt.Sprint(rpc.Verbs()) + "; A allow, D deny, H hidden\n"
		if err := os.WriteFile(filepath.Join(dir, "golden.txt"), []byte(head+strings.Join(gridRows(t, dir), "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	checkGrid(t, dir)
}

var oldPermissionsName = regexp.MustCompile(`(?m)^permissions = `)

// respelled copies the grid fixtures (and the spain policy beside them) to
// a temporary directory, passing each file's text through spell.
func respelled(t *testing.T, spell func(i int, name, text string) string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "grid")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join("testdata", "grid", "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	names = append(names, filepath.Join("testdata", "spain-policy.toml"))
	for i, src := range names {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, filepath.Base(src))
		if filepath.Base(src) == "spain-policy.toml" {
			dst = filepath.Join(root, "spain-policy.toml")
		}
		if err := os.WriteFile(dst, []byte(spell(i, filepath.Base(src), string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func toOldName(text string) string { return oldPermissionsName.ReplaceAllString(text, "grants = ") }

func TestTheGridIsTheSameUnderTheOldNameAndUnderBothNames(t *testing.T) {
	for name, spell := range map[string]func(int, string, string) string{
		"permissions": func(_ int, _, text string) string { return text },
		"grants":      func(_ int, _, text string) string { return toOldName(text) },
		"both, file by file": func(i int, _, text string) string {
			if i%2 == 0 {
				return toOldName(text)
			}
			return text
		},
		"both, role by role": func(_ int, _, text string) string {
			n := 0
			return oldPermissionsName.ReplaceAllStringFunc(text, func(m string) string {
				n++
				if n%2 == 0 {
					return "grants = "
				}
				return m
			})
		},
		"both, file by file, the other way": func(i int, _, text string) string {
			if i%2 == 1 {
				return toOldName(text)
			}
			return text
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := respelled(t, spell)
			if name != "permissions" {
				changed := 0
				for _, f := range append(gridLints, "selectors.toml", "spain-ceiling.toml", "gluck-delegated.toml") {
					data, err := os.ReadFile(filepath.Join(dir, f))
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(data), "\ngrants = ") {
						changed++
					}
				}
				if changed == 0 {
					t.Fatal("no fixture took the old name; the spelling tests nothing")
				}
			}
			checkGrid(t, dir)
		})
	}
}

func TestTheGridNoticesOnePermissionDroppedUnderTheOldName(t *testing.T) {
	dir := respelled(t, func(_ int, name, text string) string {
		if name != "spain-policy.toml" {
			return text
		}
		out := strings.Replace(toOldName(text), `"kill:sandboxed:self"`, `"read:sandboxed:self"`, 1)
		if out == toOldName(text) {
			t.Fatal("the canary found nothing to change")
		}
		return out
	})
	if diffs := gridDiff(gridGolden(t), gridRows(t, dir)); len(diffs) == 0 {
		t.Fatal("the grid did not notice a dropped permission")
	}
}

// A [figaros] table is cut out of the policy text before jkauthz reads the
// grants, so a ceiling of *:* placed anywhere must leave every unclaimed
// decision of every shipped policy as it was.
func TestACeilingOfEverythingChangesNoDecision(t *testing.T) {
	dir := filepath.Join("testdata", "grid")
	ceiling := "[figaros]\npermissions = [\"*:*\"]\n"
	placements := map[string]func(string) string{
		"appended":           func(p string) string { return p + "\n" + ceiling },
		"first, commented":   func(p string) string { return "# ceiling\n[figaros] # all\npermissions = [\n  \"*:*\",\n]\n\n" + p },
		"before every table": func(p string) string { return strings.Replace(p, "\n[", "\n"+ceiling+"\n[", 1) },
	}
	for _, c := range gridCombos {
		text := DefaultPolicy(jkauthz.Principal{Kind: jkauthz.KindUser, ID: strings.TrimPrefix(c.self, "user:")}, c.boxes)
		if c.local != "" {
			raw, err := os.ReadFile(filepath.Join(dir, c.local))
			if err != nil {
				t.Fatal(err)
			}
			text = string(raw)
		}
		want := comboRows(t, dir, c, "")
		for name, place := range placements {
			got := comboRows(t, dir, c, place(text))
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("%s, ceiling %s: decisions moved", c.name, name)
			}
		}
	}
}

func comboRows(t *testing.T, dir string, c gridCombo, policy string) []string {
	t.Helper()
	if policy != "" {
		path := filepath.Join(t.TempDir(), "policy.toml")
		if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
		c.local, c.boxes = path, nil
	}
	p := gridPolicies(t, dir, c, gridFacts())
	if policy != "" {
		if _, set := p.ceiling(); !set {
			t.Fatalf("%s: the ceiling did not load", c.name)
		}
	}
	var rows []string
	for _, text := range gridSubjects {
		s := gridSubject(t, text)
		rows = append(rows, fmt.Sprintf("hold %v", p.HoldsRole(s)))
		for _, r := range gridResources() {
			var line []byte
			for _, v := range rpc.Verbs() {
				line = append(line, gridDecision(p, s, v, r))
			}
			rows = append(rows, string(line))
		}
	}
	return rows
}

func TestACeilingTheCutCannotSetApartRefusesThePolicy(t *testing.T) {
	for name, src := range map[string]string{
		"inline":        "figaros = { permissions = [\"*:*\"] }\n" + sandboxPolicy,
		"quoted header": sandboxPolicy + "\n[\"figaros\"]\npermissions = [\"*:*\"]\n",
	} {
		p := NewPolicies(me, facts())
		if err := p.load([]byte(src)); err == nil || !strings.Contains(err.Error(), "table of its own") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
