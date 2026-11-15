package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func pagerTopologyRig(t *testing.T, line string) (*interactiveInput, *livelogTurn, *formView, *fakeMounts) {
	t.Helper()
	o, err := topologyArgs(tokenize(line))
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeMounts()
	hint := *ariasHint
	hint.Mount, hint.Sort = "id", "-active_at"
	f.hint = &hint
	f.forms["@things"] = ariasLike()
	f.plain = map[string]bool{}
	for _, id := range []string{"aaa", "bbb", "ccc", "ddd"} {
		f.forms[id], f.plain[id] = figaroForm(id), true
	}
	v := indexView(f)
	v.topology, v.subject = o, attending("bbb")
	seekAttended(v, f.open)
	var out countingWriter
	in, lt := navInput(t, &out, true)
	in.mu.Lock()
	in.installLivePit("ls", v, false)
	in.mu.Unlock()
	return in, lt, v, f
}

func TestPagerTopologyHierarchyAndMore(t *testing.T) {
	for _, more := range []bool{false, true} {
		line := "ls"
		if more {
			line += " --more"
		}
		in, lt, v, f := pagerTopologyRig(t, line)
		rows := rowsByID(v.Items(160))
		at := "here" + mountSep + "arias.top.forks.mid"
		if selectedID(in, lt) != at {
			t.Fatalf("%s selected %s", line, selectedID(in, lt))
		}
		if !strings.Contains(rows[at].str(), `"mid"`) {
			t.Errorf("expanded handle lost mantra: %s", rows[at].str())
		}
		for _, id := range []string{"arias.top.forks", "arias.top.forks.mid.forks", "arias.top.forks.mid.mantra"} {
			_, shown := rows["here"+mountSep+id]
			if shown != more {
				t.Errorf("%s row %s shown=%v", line, id, shown)
			}
		}
		_, mounted := rows[at+mountSep+"system"]
		if mounted != more {
			t.Errorf("%s mounted own form=%v; opened %v", line, mounted, opened(f))
		}
		if rows[at].aria != "bbb" || rows[at].yankText() != "mid" {
			t.Errorf("lost aria/yank: %+v", rows[at])
		}
		if !more {
			child := at + ".forks.me"
			if _, ok := rows[child]; !ok {
				t.Fatal("attended forks not expanded")
			}
			v.Collapse(child)
			if parent := v.Collapse(child); parent != at {
				t.Errorf("collapsed child goes to hidden parent %s", parent)
			}
		}
	}
}

func TestPagerTopologyScopedFormsAndCaps(t *testing.T) {
	for _, tc := range []struct{ args, want, absent string }{
		{"-H", "arias.other", ""},
		{"-a", "arias.other", ""},
		{"-n 1", "arias.other", "arias.top"},
		{"mid", "arias.mid.forks.me", "arias.top"},
		{"^", "arias.top.forks.mid", "arias.other"},
		{"^20", "arias.top", "arias.other"},
		{"/", "arias.other", ""},
	} {
		for _, suffix := range []string{"", " --more"} {
			t.Run(tc.args+suffix, func(t *testing.T) {
				_, _, v, _ := pagerTopologyRig(t, "ls "+tc.args+suffix)
				ids := strings.Join(drawnIDs(v), ",")
				if !strings.Contains(ids, tc.want) {
					t.Fatalf("missing %s: %s", tc.want, ids)
				}
				if tc.absent != "" && strings.Contains(ids, tc.absent) {
					t.Fatalf("unexpected %s: %s", tc.absent, ids)
				}
			})
		}
	}
}

func TestPagerListBytesUseNamedFormCommands(t *testing.T) {
	for _, tc := range []struct{ bytes, line string }{
		{"l", "ls"}, {"L", "ls --more"},
		{":ls -H\r", "ls -H"}, {":ls -a\r", "ls -a"}, {":ls -n 2\r", "ls -n 2"},
		{":ls -H --more\r", "ls -H --more"}, {":ls -a --more\r", "ls -a --more"},
		{":ls -n 2 --more\r", "ls -n 2 --more"}, {":ls ^2 --more\r", "ls ^2 --more"},
		{":ls -g --more\r", "ls -g --more"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			var out countingWriter
			in, lt := navInput(t, &out, true)
			var line string
			lt.tr.command = func(s string) { line = s }
			for _, b := range []byte(tc.bytes) {
				in.consume([]byte{b})
			}
			if line != tc.line {
				t.Fatalf("bytes %q routed %q, want %q", tc.bytes, line, tc.line)
			}
			p, ok, err := topologyFormPit(tokenize(line))
			if err != nil || !ok || p.form != rpc.TroupeFormID || !opensPit(line) {
				t.Fatalf("not a form pit: %+v %v %v", p, ok, err)
			}
		})
	}
}

func TestPagerTopologyActionsThroughTheByteDispatcher(t *testing.T) {
	for _, suffix := range []string{"", " --more"} {
		in, lt, v, f := pagerTopologyRig(t, "ls"+suffix)
		var attended string
		lt.tr.attendAria = func(id string) { attended = id }
		v.act = func(a rpc.ViewAction) { pitAct(in, a, nil) }
		in.consume([]byte{'\r'})
		if attended != "bbb" {
			t.Fatalf("Enter attended %s", attended)
		}
		in.consume([]byte{'x'})
		if sent := f.waitSent(t, 0); sent != "" {
			t.Fatalf("x killed without confirmation: %s", sent)
		}
		in.consume([]byte{'x'})
		if sent := f.waitSent(t, 1); sent != "@things "+rpc.MethodKill+" bbb" {
			t.Fatalf("confirmed kill sent %s", sent)
		}
	}
}

func TestPagerTopologySelectionSurvivesReparentingIntoAClosedBranch(t *testing.T) {
	for _, suffix := range []string{"", " --more"} {
		in, lt, v, f := pagerTopologyRig(t, "ls"+suffix)
		src := f.forms["@things"]
		src.snap = things(
			"arias.top.id", `"aaa"`, "arias.top.mantra", `"top"`,
			"arias.other.id", `"ddd"`, "arias.other.mantra", `"other"`,
			"arias.other.forks.mid.id", `"bbb"`, "arias.other.forks.mid.mantra", `"mid"`,
			"arias.other.forks.mid.forks.me.id", `"ccc"`, "arias.other.forks.mid.forks.me.mantra", `"me"`,
		).snap
		in.mu.Lock()
		lt.tr.pit.refreshLive(100)
		in.mu.Unlock()
		want := "here" + mountSep + "arias.other.forks.mid"
		if got := selectedID(in, lt); got != want {
			t.Fatalf("%s reparenting moved selection to %s, want %s", suffix, got, want)
		}
		rows := rowsByID(v.Items(100))
		if _, ok := rows[want+".forks.me"]; !ok {
			t.Fatal("reparented selection lost its expansion")
		}
	}
}

func TestPagerTopologySelectionSurvivesLiveChange(t *testing.T) {
	in, lt, v, f := pagerTopologyRig(t, "ls")
	want := selectedID(in, lt)
	before := lt.tr.pit.list().cursor
	src := f.forms["@things"]
	src.snap = src.snap.SetPath("arias.top.active_at", []byte(`99999999999`))
	in.mu.Lock()
	lt.tr.pit.refreshLive(100)
	in.mu.Unlock()
	if got := selectedID(in, lt); got != want {
		t.Fatalf("update moved selection %q -> %q", want, got)
	}
	if lt.tr.pit.list().cursor == before {
		t.Fatal("fixture did not reorder the live hierarchy")
	}
	rows := rowsByID(v.Items(100))
	if !strings.Contains(rows[want].str(), `"mid"`) {
		t.Fatal("live update lost attended mantra")
	}
}
