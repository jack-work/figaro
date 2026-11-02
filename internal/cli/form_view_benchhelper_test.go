package cli

import (
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func newBenchFormView(snap form.Snapshot) (itemView, func(form.Patch)) {
	m := &formMirror{}
	m.reset(snap, 1, 1)
	v := &formView{mirror: m, open: map[string]bool{}}
	version := uint64(1)
	return v, func(p form.Patch) {
		version++
		m.apply(rpc.FormDelta{Schema: rpc.FormDeltaSchema, Incarnation: 1, Version: version, Patch: p})
	}
}

func benchOpenAll(iv itemView) {
	v := iv.(*formView)
	var walk func(n *formNode)
	walk = func(n *formNode) {
		for _, c := range n.kids() {
			if !c.leaf() {
				v.open[c.path] = true
				walk(c)
			}
		}
	}
	walk(newFormRoot(v.source().Shown()))
	v.openRev++
}
