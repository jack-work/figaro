package cli

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func TestShownFormEditParses(t *testing.T) {
	base := form.FromMap(formKeys("a", `1`, "b.c", `"x"`))
	cases := []struct {
		line   string
		ok     bool
		id     string
		after  string
		absent string
	}{
		{line: `set a 2`, ok: true, after: `{"a":2,"b":{"c":"x"}}`},
		{line: `form set b.c 'hello world'`, ok: true, after: `{"a":1,"b":{"c":"hello world"}}`},
		{line: `state set --id x1 n '{"k":[1]}'`, ok: true, id: "x1", after: `{"a":1,"b":{"c":"x"},"n":{"k":[1]}}`},
		{line: `form delete a,b.c`, ok: true, after: `{"b":{}}`},
		{line: `unset --id=x2 a`, ok: true, id: "x2", after: `{"b":{"c":"x"}}`},
		{line: `form set a=1,b=2`, ok: false},
		{line: `set -j a 1`, ok: false},
		{line: `set a[0] 1`, ok: false},
		{line: `form ls`, ok: false},
		{line: `set a`, ok: false},
	}
	for _, tc := range cases {
		p, id, ok := shownFormEdit(tokenize(tc.line), base)
		if ok != tc.ok {
			t.Fatalf("%s: routed=%v, want %v", tc.line, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if id != tc.id {
			t.Fatalf("%s: id %q, want %q", tc.line, id, tc.id)
		}
		got, _ := base.Apply(p).MarshalJSON()
		if string(got) != tc.after {
			t.Fatalf("%s: form becomes %s, want %s", tc.line, got, tc.after)
		}
	}
}

// c fills the : line with a command that, run as typed, sets the same value.
func TestChangeLineRoundTrips(t *testing.T) {
	values := []string{`"plain"`, `"it's quoted \"twice\""`, `[1, {"b": "x y"}]`, `42`, `"back\\slash"`, `""`, `"tab\there"`}
	for _, val := range values {
		src := &fakeFormSource{snap: form.FromMap(formKeys("k.v", val))}
		v := &formView{src: src, open: map[string]bool{"k": true}, aria: "host"}
		v.openRev++
		v.Items(80)
		line, note := v.changeLine("k.v", "host")
		if line == "" {
			t.Fatalf("%s: no line (%s)", val, note)
		}
		if strings.Contains(line, "--id") {
			t.Fatalf("the subject's own form needs no --id: %s", line)
		}
		p, _, ok := shownFormEdit(tokenize(line), src.snap)
		if !ok {
			t.Fatalf("%s: the filled line does not route: %s", val, line)
		}
		if !p.IsIdentity() {
			t.Fatalf("%s: running the line unchanged would change the form: %s", val, line)
		}
	}
	src := &fakeFormSource{snap: form.FromMap(formKeys("k", `1`))}
	v := &formView{src: src, open: map[string]bool{}, aria: "other"}
	v.Items(80)
	if line, _ := v.changeLine("k", "host"); !strings.Contains(line, "--id other") {
		t.Fatalf("another aria's form must be named: %s", line)
	}
	ro := &formView{mirror: &formMirror{}, open: map[string]bool{}}
	if line, note := ro.changeLine("k", "host"); line != "" || note == "" {
		t.Fatalf("a read-only form offered a line %q (%q)", line, note)
	}
}

func TestFormViewEditsThroughItsSource(t *testing.T) {
	src := &fakeFormSource{snap: form.FromMap(formKeys("a", `1`))}
	v := &formView{src: src, open: map[string]bool{}}
	p, _, _ := shownFormEdit(tokenize("set a 5"), src.snap)
	if err := v.Edit(p); err != nil {
		t.Fatal(err)
	}
	if got := formRowText(v.Items(80), "a"); !strings.Contains(got, "5") {
		t.Fatalf("the edit is not on screen: %q", got)
	}
	m := &formMirror{}
	m.reset(form.FromMap(formKeys("a", `1`)), 1, 1)
	if err := (&formView{mirror: m}).Edit(p); err == nil {
		t.Fatal("a bare mirror accepted an edit it cannot send")
	}
}

// c in a form pit fills the : line through the real key path; any other pit
// keeps the old meaning of c.
func TestChangeKeyFillsTheCommandLine(t *testing.T) {
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	src := &fakeFormSource{snap: form.FromMap(formKeys("mantra", `"old words"`, "n", `1`))}
	v := &formView{src: src, open: map[string]bool{}, aria: "aria1234"}
	v.change = func() { pitChange(tr) }
	tr.showLivePit("form show", v, false)
	tr.pit.lines(100, 20)
	tr.key('c')
	if tr.box != boxCommand {
		t.Fatalf("c did not open the : line (box %v)", tr.box)
	}
	if got := tr.cmdline.String(); got != `form set mantra '"old words"'` {
		t.Fatalf("the line reads %q", got)
	}
	p, id, ok := shownFormEdit(tokenize(tr.cmdline.String()), src.snap)
	if !ok || id != "" || !p.IsIdentity() {
		t.Fatalf("the filled line does not round-trip: ok=%v id=%q", ok, id)
	}
}
