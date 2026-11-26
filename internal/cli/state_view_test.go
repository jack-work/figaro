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

func stateRig(t *testing.T, keys map[string]string, open ...string) (*transcript, *formView) {
	t.Helper()
	kv := []string{}
	for k, v := range keys {
		kv = append(kv, k, v)
	}
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	v := &formView{src: &fakeFormSource{snap: form.FromMap(formKeys(kv...))}, open: map[string]bool{}, aria: "aria1234"}
	for _, o := range open {
		v.open[o] = true
	}
	v.openRev++
	tr.showLivePit("form show", v, false)
	tr.pit.lines(100, 20)
	return tr, v
}

func stateRows(v *formView) []string {
	var out []string
	for _, r := range v.Items(100) {
		out = append(out, strings.TrimRight(stripANSI(r.str()), " "))
	}
	return out
}

func TestStateListEntriesTakeOneLineEach(t *testing.T) {
	tr, v := stateRig(t, map[string]string{"runtime.route": `["rou2","anthropic-max","claude-opus-5-5"]`}, "runtime")
	tr.pit.focus("runtime.route")
	tr.key('\r')
	got := stateRows(v)
	tail := got[len(got)-3:]
	for i, want := range []string{"rou2", "anthropic-max", "claude-opus-5-5"} {
		if strings.TrimSpace(tail[i]) != want {
			t.Fatalf("rows %q", got)
		}
	}
	for _, l := range got {
		if strings.TrimSpace(l) == "[" || strings.TrimSpace(l) == "]" {
			t.Fatalf("bracket rows remain: %q", got)
		}
	}
}

func studiedRig(t *testing.T) (*transcript, *formView, *[]string, *[]string) {
	t.Helper()
	tr, v := stateRig(t, map[string]string{"runtime.studies": `["@role1","@plain1"]`}, "runtime", "runtime.studies")
	var opened, attended []string
	v.opener = func(addr string) (*formView, actOn, func(), <-chan struct{}, error) {
		opened = append(opened, addr)
		inner := &formView{src: &fakeFormSource{snap: form.FromMap(formKeys("brief", `"inner brief"`))}, open: map[string]bool{}}
		return inner, nil, func() {}, nil, nil
	}
	tr.attendAria = func(id string) { attended = append(attended, id) }
	return tr, v, &opened, &attended
}

func TestStudiesAreRowsYouCanExpandIntoTheStudiedForm(t *testing.T) {
	tr, v, opened, _ := studiedRig(t)
	tr.pit.focus("runtime.studies\x001")
	tr.key('l')
	var got []string
	for i := 0; i < 50 && !strings.Contains(strings.Join(got, "\n"), "inner brief"); i++ {
		time.Sleep(10 * time.Millisecond)
		got = stateRows(v)
	}
	if !strings.Contains(strings.Join(got, "\n"), "inner brief") {
		t.Fatalf("the studied form did not open under its entry: %q", got)
	}
	if len(*opened) != 1 || (*opened)[0] != "@plain1" {
		t.Fatalf("opened %q", *opened)
	}
	tr.key('h')
	if strings.Contains(strings.Join(stateRows(v), "\n"), "inner brief") {
		t.Fatal("h left the studied form open")
	}
}

func TestEnterOnAStudiedRoleAttendsIt(t *testing.T) {
	tr, _, _, attended := studiedRig(t)
	tr.pit.focus("runtime.studies\x000")
	tr.key('\r')
	if len(*attended) != 1 || (*attended)[0] != "@role1" {
		t.Fatalf("attended %q", *attended)
	}
}

func TestCapitalAOpensTheStateOfARowWithoutAttendingIt(t *testing.T) {
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	v := recencyRig()
	v.open["arias"] = true
	v.openRev++
	var opened, attended []string
	tr.openState = func(id string) { opened = append(opened, id) }
	tr.attendAria = func(id string) { attended = append(attended, id) }
	tr.showLivePit("ls", v, false)
	tr.pit.lines(100, 20)
	tr.pit.focus("arias.holder")
	tr.key('A')
	if len(opened) != 1 || opened[0] != "d" || len(attended) != 0 {
		t.Fatalf("opened %q attended %q", opened, attended)
	}
	tr.key('a')
	if len(attended) == 0 && len(opened) != 1 {
		t.Fatalf("a moved the state: opened %q", opened)
	}
}

func TestCapitalAIsBoundOnceInThePitLayers(t *testing.T) {
	n := 0
	for _, l := range defaultLayers {
		if l.modes&(inList|inPit) == 0 {
			continue
		}
		for _, b := range l.binds {
			for _, k := range strings.Fields(b.keys) {
				if k == "A" {
					n++
				}
			}
		}
	}
	if n != 1 {
		t.Fatalf("A is bound %d times across the list and pit layers", n)
	}
}
