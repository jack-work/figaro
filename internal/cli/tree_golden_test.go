package cli

import (
	"testing"

	"github.com/jack-work/figaro/internal/rpc"
)

// treeFixture is a shape with everything the walkers have to get right: a
// root with three children, a middle child that itself branches, a second
// top-level tree, and recency ordering that differs from vector ordering.
// LastActive values are negative on purpose: recency is decided by comparison,
// which negatives preserve, while relAge prints "-" for anything <= 0: so the
// golden below does not change with the calendar.
func treeFixture() []rpc.FigaroInfoResponse {
	return []rpc.FigaroInfoResponse{
		{ID: "aaaa1111", Vector: []int{0}, Mantra: "the first tree", LastActive: -400,
			OutfitName: "opus5-ant", OutfitVer: "v1", MessageCount: 12, ContextTokens: 12000, ContextExact: true, Cwd: "/home/x/dev/figaro", State: "idle", Kind: "conversation", Parent: "outfit-node"},
		{ID: "bbbb2222", Vector: []int{0, 0}, Mantra: "first child", LastActive: -100,
			OutfitName: "opus5-ant", OutfitVer: "v1", MessageCount: 3, ContextTokens: 900, Cwd: "/home/x/dev/figaro", State: "active", Kind: "conversation", Parent: "aaaa1111", BranchedLT: 9},
		{ID: "cccc3333", Vector: []int{0, 1}, Mantra: "second child which has a very long mantra indeed", LastActive: -200,
			OutfitName: "sonn5", OutfitVer: "v2", MessageCount: 7, ContextTokens: 4000, Cwd: "/tmp", State: "idle", Kind: "conversation", Parent: "aaaa1111", BranchedLT: 4},
		{ID: "dddd4444", Vector: []int{0, 1, 0}, Mantra: "grandchild", LastActive: -250,
			OutfitName: "sonn5", OutfitVer: "v2", MessageCount: 1, Cwd: "/tmp", State: "idle", Kind: "conversation", Parent: "cccc3333", BranchedLT: 2},
		{ID: "eeee5555", Vector: []int{1}, Mantra: "", LastActive: -500,
			OutfitName: "", OutfitVer: "", MessageCount: 0, Cwd: "", State: "idle", Kind: "conversation", Parent: "null"},
	}
}

func globalFixture() []rpc.FigaroInfoResponse {
	figs := treeFixture()
	return append(figs,
		rpc.FigaroInfoResponse{ID: "null", Kind: "null", Parent: "", LastActive: -900},
		rpc.FigaroInfoResponse{ID: "outfit-node", Kind: "outfit", OutfitName: "opus5-ant", OutfitVer: "live", Parent: "null", LastActive: -50},
	)
}

// renderFixture is the whole pipeline for a fixed tree at a fixed width:
// walk, then lay out. It is the user-visible contract, so it is what the
// refactor onto figtree must hold constant.
func renderFixture(t *testing.T, width int, global bool) string {
	t.Helper()
	if global {
		return renderListRows(globalTree(globalFixture(), "", 0).Rows(), width, true)
	}
	tree, _ := listTree(treeFixture(), "", 0)
	return renderListRows(tree.Rows(), width, false)
}

func TestRenderedListGolden(t *testing.T) {
	for _, tc := range []struct {
		name   string
		width  int
		global bool
		want   string
	}{
		{"compact", 80, false, `○ the first tree aaaa1111 12msg
├─▸ first child bbbb2222 3msg
└─○ second child which has a very long mantra .. cccc3333 7msg
  └─○ grandchild dddd4444 1msg
○ aria eeee5555 eeee5555 0msg
`},
		{"reduced", 120, false, `ARIA                                              ID        OUTFIT     AGE  MSGS  CTX
○ the first tree                                  aaaa1111  opus5-ant  -    12    12k
├─▸ first child                                   bbbb2222  opus5-ant  -    3     ~900
└─○ second child which has a very long mantra ..  cccc3333  sonn5      -    7     ~4k
  └─○ grandchild                                  dddd4444  sonn5      -    1     -
○ aria eeee5555                                   eeee5555  -          -    0     -
`},
		{"full", 160, false, `ARIA                                              ID        OUTFIT     VER  FORK  AGE  MSGS  CTX   CWD
○ the first tree                                  aaaa1111  opus5-ant  v1   -     -    12    12k   /home/x/dev/figaro
├─▸ first child                                   bbbb2222  opus5-ant  v1   yes   -    3     ~900  /home/x/dev/figaro
└─○ second child which has a very long mantra ..  cccc3333  sonn5      v2   yes   -    7     ~4k   /tmp
  └─○ grandchild                                  dddd4444  sonn5      v2   yes   -    1     -     /tmp
○ aria eeee5555                                   eeee5555  -          -    -     -    0     -     -
`},
		{"global", 120, true, `ARIA                                                    ID           DETAIL
○ null                                                  null         genesis root · ceremonial
├─● outfit opus5-ant@live                               outfit-node  ceremonial
│ └─○ the first tree                                    aaaa1111     12 msgs
│   ├─▸ first child                                     bbbb2222     3 msgs
│   └─○ second child which has a very long mantra in..  cccc3333     7 msgs
│     └─○ grandchild                                    dddd4444     1 msgs
└─○ aria eeee5555                                       eeee5555     0 msgs
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderFixture(t, tc.width, tc.global); got != tc.want {
				t.Fatalf("rendered list moved:\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
		})
	}
}
