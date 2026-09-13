package aria

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

func gapModelSource(first, last, revision int) []Turn {
	out := make([]Turn, 0, last-first+1)
	for i := first; i <= last; i++ {
		nodes := make([]livedoc.Node, 1+i%3)
		for j := range nodes {
			nodes[j] = livedoc.Node{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("r%d/t%d/n%d", revision, i, j)}
		}
		if i%4 == 0 {
			nodes = nil
		}
		out = append(out, Turn{ID: uint64(i), Sealed: true, Inquiry: fmt.Sprintf("r%d/Q%d", revision, i), Nodes: nodes})
	}
	return out
}

func FuzzForkCloneReadCoverage(f *testing.F) {
	for _, seed := range [][]byte{{1, 0, 0, 0, 1, 0, 0, 0, 1}, {0, 8, 0, 1, 4, 0, 0, 8, 0}, {0, 0, 1, 1, 2, 3, 0, 4, 5, 0, 1, 2}, {1, 3, 2, 0, 3, 1, 0, 6, 2}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 300 {
			ops = ops[:300]
		}
		source := gapModelSource(1, 9, 0)
		c := NewClient()
		held := map[Anchor]string{}
		more := More{}
		for i := 0; i+2 < len(ops); i += 3 {
			action, a, b := ops[i]%3, int(ops[i+1]), int(ops[i+2])
			switch action {
			case 0:
				at := Anchor{Turn: uint64(1 + a%(len(source)+2)), Node: uint64(b % 4)}
				p := PaginateBefore(source, at, Anchor{}, 100+(a+b)%900)
				c.Apply(p, Notify)
				for _, part := range p.Parts {
					if len(part.Nodes) == 0 {
						held[Anchor{Turn: part.ID, Node: 0}] = "inquiry:" + part.Inquiry
					}
					for k, n := range part.Nodes {
						held[Anchor{Turn: part.ID, Node: part.From + uint64(k)}] = n.Markdown
					}
				}
				if len(p.Parts) > 0 {
					lo, hi := Anchor{Turn: ^uint64(0), Node: ^uint64(0)}, Anchor{}
					for at := range held {
						if at.Less(lo) {
							lo = at
						}
						if hi.Less(at) {
							hi = at
						}
					}
					pageLo, pageHi := Anchor{Turn: ^uint64(0), Node: ^uint64(0)}, Anchor{}
					for _, part := range p.Parts {
						pLo := Anchor{Turn: part.ID, Node: part.From}
						pHi := Anchor{Turn: part.ID, Node: part.From + uint64(max(1, len(part.Nodes))-1)}
						if pLo.Less(pageLo) {
							pageLo = pLo
						}
						if pageHi.Less(pHi) {
							pageHi = pHi
						}
					}
					if !lo.Less(pageLo) {
						more.Before = p.More.Before
						c.SetMoreBefore(p.More.Before)
					}
					if !pageHi.Less(hi) {
						more.After = p.More.After
					}
				}
			case 1:
				base := 1 + a%(len(source)+1)
				c = c.CloneBelow(base)
				for at := range held {
					if at.Turn >= uint64(base) {
						more.After = true
						delete(held, at)
					}
				}
				source = append(source[:base-1:base-1], gapModelSource(base, base+b%4, i+1)...)
			case 2:
				floor := Anchor{Turn: uint64(1 + a%len(source)), Node: uint64(b % 3)}
				c.EvictBefore(floor)
				for at := range held {
					if at.Less(floor) {
						delete(held, at)
					}
				}
			}
			if got := c.Store().More(); got != more {
				t.Fatalf("step %d ops=%v: edge knowledge %+v, model %+v", i, ops[:i+3], got, more)
			}
			complete := true
			for _, turn := range source {
				for k := range max(1, len(turn.Nodes)) {
					at := Anchor{Turn: turn.ID, Node: uint64(k)}
					want, wantHeld := held[at]
					complete = complete && wantHeld
					var got string
					gotHeld := false
					for _, seg := range c.Query(at, at) {
						if seg.Gap != nil && wantHeld {
							t.Fatalf("step %d ops=%v: false gap at held %v", i, ops[:i+3], at)
						}
						for _, m := range seg.Msgs {
							if len(m.Nodes) == 0 && uint64(m.Turn) == at.Turn && at.Node == 0 {
								got, gotHeld = "inquiry:"+m.Inquiry, true
							}
							for n, v := range m.Nodes {
								if uint64(m.Turn) == at.Turn && m.From+uint64(n) == at.Node {
									got, gotHeld = v.Markdown, true
								}
							}
						}
					}
					if gotHeld != wantHeld || gotHeld && got != want {
						t.Fatalf("step %d ops=%v: %v got %q/%v want %q/%v", i, ops[:i+3], at, got, gotHeld, want, wantHeld)
					}
				}
			}
			c.ForEachSegment(Anchor{}, Anchor{Turn: ^uint64(0), Node: ^uint64(0)}, func(Message) bool { return true }, func(g Gap) bool {
				if g.From.Turn == 0 || complete {
					t.Fatalf("step %d ops=%v: iterator invented gap %v..%v (complete=%v)", i, ops[:i+3], g.From, g.To, complete)
				}
				return true
			})
			if complete {
				for _, seg := range c.Query(Anchor{Turn: 1}, Anchor{Turn: ^uint64(0), Node: ^uint64(0)}) {
					if seg.Gap != nil {
						t.Fatalf("step %d ops=%v: complete source has phantom gap %v..%v, More=%+v", i, ops[:i+3], seg.Gap.From, seg.Gap.To, c.Store().More())
					}
				}
			}
		}
	})
}
