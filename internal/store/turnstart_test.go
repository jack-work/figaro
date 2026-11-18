package store

import (
	"math/rand"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/turns"
)

// TurnStart must answer what turns.Span answers over the whole log, for every
// turn of every node of random fork chains, including turns past the end and
// the zero-stamped birth records at each fork base.
func TestTurnStartAgreesWithSpan(t *testing.T) {
	for seed := int64(1); seed <= 30; seed++ {
		rng := rand.New(rand.NewSource(seed))
		be, cur, log := newConversation(t)
		chain := []string{cur}
		turn := uint64(0)
		write := func(l arialog.Log[message.Message], n int) {
			for i := 0; i < n; i++ {
				turn++
				put(t, l, ask(turn, "q"))
				for r := 0; r < rng.Intn(4); r++ {
					if rng.Intn(2) == 0 {
						put(t, l, answer(turn, "thinking"))
					} else {
						put(t, l, message.Message{Role: message.RoleInput})
					}
				}
				put(t, l, answer(turn, "a"))
			}
		}
		write(log, 2+rng.Intn(6))
		for hop := 0; hop < 1+rng.Intn(4); hop++ {
			at := uint64(0)
			if rng.Intn(2) == 0 {
				tail, _ := log.ReadPage(0, 1<<62, 1)
				at = 1 + uint64(rng.Int63n(int64(tail[0].LT)))
			}
			child, _, err := be.ForkWith(cur, at, birth("c"))
			if err != nil {
				t.Fatal(err)
			}
			cur = child
			chain = append(chain, cur)
			log, _ = be.OpenFigIR(cur)
			if rng.Intn(3) != 0 {
				turn = lastTurn(t, log)
				write(log, 1+rng.Intn(3))
			}
		}
		for _, id := range chain {
			l, err := be.OpenFigIR(id)
			if err != nil {
				t.Fatal(err)
			}
			all := readAll(l)
			wantLast := turns.StampIDs(all)
			for n := uint64(0); n <= wantLast+2; n++ {
				first, last, ok := TurnStart(l, n)
				wantFirst, _, wantOK := turns.Span(all, n)
				if n == 0 {
					wantOK = false
				}
				if ok != wantOK || (ok && first != wantFirst) || last != wantLast {
					t.Fatalf("seed %d aria %s turn %d: TurnStart says (%d, last %d, %v), Span says (%d, last %d, %v)",
						seed, id, n, first, last, ok, wantFirst, wantLast, wantOK)
				}
			}
		}
	}
}
