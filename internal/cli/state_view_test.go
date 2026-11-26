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
