package cli

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

// The shape of l on a large store: 400 figaros sorted by -active_at, a quarter
// of them with three forks, and the attended figaro opened whole with its
// form mounted under it. key is one held-j keystroke: move, then the
// measuring and drawing a frame does. delta is one figaro's active_at moving,
// then the frame that redraws it.
func BenchmarkTopologyPit(b *testing.B) {
	hint := *ariasLikeHint
	hint.Mount, hint.Sort = "id", "-active_at"
	f := newFakeMounts()
	f.hint, f.plain = &hint, map[string]bool{}
	var kv []string
	for i := range 400 {
		at := fmt.Sprintf("arias.a%03d.", i)
		id := fmt.Sprintf("f%03d", i)
		kv = append(kv, at+"id", `"`+id+`"`, at+"mantra", `"figaro number `+id+`"`, at+"active_at", fmt.Sprint(1_700_000_000+i*37%400))
		f.forms[id], f.plain[id] = figaroForm("mantra of "+id), true
		if i%4 != 0 {
			continue
		}
		for j := range 3 {
			fat := fmt.Sprintf("%sforks.k%d.", at, j)
			fid := fmt.Sprintf("%s-%d", id, j)
			kv = append(kv, fat+"id", `"`+fid+`"`, fat+"mantra", `"fork `+fid+`"`, fat+"active_at", fmt.Sprint(1_700_000_000+j))
			f.forms[fid], f.plain[fid] = figaroForm("mantra of "+fid), true
		}
	}
	src := things(kv...)
	f.forms["@things"] = src
	v := indexView(f)
	v.subject = attending("f200")
	seekAttended(v, f.open)
	v.seekMarked()
	var d pit
	d.showLive(pitTroupe, v)
	d.fit(200)
	d.lines(200, 40)
	frame := func() {
		d.fit(200)
		d.lines(200, 40)
	}
	b.Run("key", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			if i%300 == 0 {
				d.toTop()
			}
			d.moveSelection(1)
			frame()
		}
	})
	b.Run("delta", func(b *testing.B) {
		b.ReportAllocs()
		for i := range b.N {
			src.snap = src.snap.Apply(form.Build(src.snap, map[string]json.RawMessage{
				"arias.a123.active_at": json.RawMessage(fmt.Sprint(1_800_000_000 + i)),
			}, nil))
			frame()
		}
	})
}
