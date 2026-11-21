package aria

import "testing"

func TestUntilNextAgreesWindowedAndFull(t *testing.T) {
	turns := floorTurns(40)
	bounded, _ := floorServer(t, turns, 32<<10)
	full := NewServer()
	for _, tn := range turns {
		full.Commit(tn)
	}
	for u := uint64(1); u <= 42; u++ {
		for _, node := range []uint64{0, 1, 2} {
			until := Anchor{Turn: u, Node: node}
			for _, at := range []Anchor{{}, {Turn: 5}, {Turn: 11, Node: 1}, {Turn: 30}} {
				for _, budget := range []int{600, 4 << 10, 1 << 16} {
					a := bounded.ReadUntil(at, until, budget)
					b := full.ReadUntil(at, until, budget)
					if !sameIDs(partIDs(a), partIDs(b)) || a.More != b.More || (a.Next == nil) != (b.Next == nil) || (a.Next != nil && *a.Next != *b.Next) {
						t.Errorf("until=%+v at=%+v budget=%d: %v %+v %v vs %v %+v %v", until, at, budget, partIDs(a), a.More, a.Next, partIDs(b), b.More, b.Next)
					}
					if a.More.After && a.Next == nil {
						t.Errorf("until=%+v at=%+v budget=%d: More.After with no Next (%v)", until, at, budget, partIDs(a))
					}
				}
			}
		}
	}
}

func TestUntilAndTheOpenTail(t *testing.T) {
	turns := floorTurns(40)
	for _, srvOf := range []func() *Server{
		func() *Server { s, _ := floorServer(t, turns, 32<<10); return s },
		func() *Server {
			s := NewServer()
			for _, tn := range turns {
				s.Commit(tn)
			}
			return s
		},
	} {
		s := srvOf()
		s.OpenInquiry(41, "live", nil)
		for _, c := range []struct {
			until Anchor
			want  uint64
		}{{Anchor{Turn: 41}, 40}, {Anchor{Turn: 42}, 41}, {Anchor{}, 41}, {Anchor{Turn: 40}, 39}} {
			p := s.ReadUntil(Anchor{Turn: 35}, c.until, 1<<20)
			ids := partIDs(p)
			if len(ids) == 0 || ids[len(ids)-1] != c.want {
				t.Errorf("until %+v: %v More=%+v Next=%v", c.until, ids, p.More, p.Next)
			}
		}
	}
}
