package angelus

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// The hop's blocking read. Two shapes: head, each link a head fork whose base
// sits past the parent's end; interior, each link cut at a turn the parent has.
// unrelated is how many other arias share the store.
func BenchmarkLineageRead(b *testing.B) {
	for _, history := range []int{300, 3000} {
		for _, unrelated := range []int{0, 1000} {
			var fx *hopFixture // built on first use, so a -bench filter skips it
			for _, shape := range []string{"head", "interior"} {
				for _, depth := range []int{2, 4} {
					name := fmt.Sprintf("shape=%s/depth=%d/history=%d/unrelated=%d", shape, depth, history, unrelated)
					b.Run(name, func(b *testing.B) {
						if fx == nil {
							fx = newHopFixture(b, history, unrelated)
						}
						leaf, parent := fx.pair(shape, depth)
						params, _ := json.Marshal(rpc.LineageRequest{FigaroID: leaf, Against: parent})
						// One read outside the timer: the first read records the base turn.
						if _, err := fx.h.lineage(inProcess(b.Context()), params); err != nil {
							b.Fatal(err)
						}
						b.ReportAllocs()
						for b.Loop() {
							if _, err := fx.h.lineage(inProcess(b.Context()), params); err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			}
		}
	}
}

func BenchmarkForkWith(b *testing.B) {
	for _, history := range []int{300, 3000} {
		b.Run(fmt.Sprintf("history=%d", history), func(b *testing.B) {
			fx := newHopFixture(b, history, 0)
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				i++
				if _, _, err := fx.be.ForkWith(fx.root, 0, benchBirth(fmt.Sprintf("f%d", i))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type hopFixture struct {
	be     *store.XwalBackend
	h      *handlers
	root   string
	chains map[string][]string // shape -> root, link1, link2, ...
}

func benchBirth(id string) message.Patch {
	raw, _ := json.Marshal(id)
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": raw}, nil)
}

func newHopFixture(b *testing.B, history, unrelated int) *hopFixture {
	b.Helper()
	be, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { be.Close() })
	for i := 0; i < unrelated; i++ {
		if _, _, err := be.ForkWith("", 0, benchBirth(fmt.Sprintf("u%d", i))); err != nil {
			b.Fatal(err)
		}
	}
	root, _, err := be.ForkWith("", 0, benchBirth("root"))
	if err != nil {
		b.Fatal(err)
	}
	prose := strings.Repeat("a sentence of ordinary length in a reply. ", 24) // ~1 KB
	write := func(id string, turns int, from uint64) uint64 {
		log, err := be.OpenFigIR(id)
		if err != nil {
			b.Fatal(err)
		}
		t := from
		for i := 0; i < turns; i++ {
			t++
			for _, m := range []message.Message{
				{Role: message.RoleInput, TurnID: t, Content: []message.Content{message.TextContent("question")}},
				{Role: message.RoleOutput, TurnID: t, Content: []message.Content{message.TextContent(prose)}},
				{Role: message.RoleOutput, TurnID: t, Content: []message.Content{message.TextContent(prose)}},
			} {
				if _, err := log.Append(store.Entry[message.Message]{Payload: m}); err != nil {
					b.Fatal(err)
				}
			}
		}
		return t
	}
	turn := write(root, history/3, 0)

	fx := &hopFixture{be: be, h: &handlers{angelus: &Angelus{Backend: be, uiProj: uiir.New(nil)}}, root: root,
		chains: map[string][]string{}}
	for _, shape := range []string{"head", "interior"} {
		chain := []string{root}
		cur, t := root, turn
		for d := 0; d < 4; d++ {
			at := uint64(0) // head: the parent's tail
			if shape == "interior" {
				// Cut the parent at its last turn: the base is a real record.
				log, _ := be.OpenFigIR(cur)
				page, _ := log.ReadPage(0, 1<<62, 6)
				for i := len(page) - 1; i >= 0; i-- {
					if page[i].Payload.Role == message.RoleInput && page[i].Payload.TurnID == t {
						at = page[i].LT - 1
						break
					}
				}
			}
			child, _, err := be.ForkWith(cur, at, benchBirth(fmt.Sprintf("%s%d", shape, d)))
			if err != nil {
				b.Fatal(err)
			}
			if shape == "interior" {
				t-- // the child replaces the turn it was cut at
			}
			t = write(child, 1, t)
			chain = append(chain, child)
			cur = child
		}
		fx.chains[shape] = chain
	}
	return fx
}

func (fx *hopFixture) pair(shape string, depth int) (leaf, parent string) {
	c := fx.chains[shape]
	return c[depth], c[depth-1]
}
