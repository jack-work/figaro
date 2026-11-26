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
