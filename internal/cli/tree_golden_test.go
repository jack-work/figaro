package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

// LastActive values are negative on purpose: comparison still orders them,
// and relAge prints "-" for anything <= 0, so the golden does not age.
// Handle and Short are what the daemon's listing carries for each row.
func treeFixture() []rpc.FigaroInfoResponse {
	figs := []rpc.FigaroInfoResponse{
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
	for i := range figs {
		figs[i].Handle = handle.Words(figs[i].ID, handle.MinWords)
		figs[i].Short = figs[i].ID[:handle.HexLen]
	}
	return figs
}

func globalFixture() []rpc.FigaroInfoResponse {
	figs := treeFixture()
	return append(figs,
		rpc.FigaroInfoResponse{ID: "null", Kind: "null", Parent: "", LastActive: -900},
		rpc.FigaroInfoResponse{ID: "outfit-node", Kind: "outfit", OutfitName: "opus5-ant", OutfitVer: "live", Parent: "null", LastActive: -50},
	)
}

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
		{"compact", 80, false, `○ the first tree onward-office 12msg
├─▸ first child pushup-upright 3msg
└─○ second child which has a very long mantra .. senator-copier 7msg
  └─○ grandchild tallness-hefty 1msg
○ aria username-prune username-prune 0msg
`},
		{"reduced", 120, false, `ARIA                                              ID              OUTFIT     AGE  MSGS  CTX
○ the first tree                                  onward-office   opus5-ant  -    12    12k
├─▸ first child                                   pushup-upright  opus5-ant  -    3     ~900
└─○ second child which has a very long mantra ..  senator-copier  sonn5      -    7     ~4k
  └─○ grandchild                                  tallness-hefty  sonn5      -    1     -
○ aria username-prune                             username-prune  -          -    0     -
`},
		{"full", 160, false, `ARIA                                              ID              OUTFIT     VER  FORK  AGE  MSGS  CTX   CWD
○ the first tree                                  onward-office   opus5-ant  v1   -     -    12    12k   /home/x/dev/figaro
├─▸ first child                                   pushup-upright  opus5-ant  v1   yes   -    3     ~900  /home/x/dev/figaro
└─○ second child which has a very long mantra ..  senator-copier  sonn5      v2   yes   -    7     ~4k   /tmp
  └─○ grandchild                                  tallness-hefty  sonn5      v2   yes   -    1     -     /tmp
○ aria username-prune                             username-prune  -          -    -     -    0     -     -
`},
		{"global", 120, true, `ARIA                                                    ID              DETAIL
○ null                                                  null            genesis root · ceremonial
├─● outfit opus5-ant@live                               outfit-node     ceremonial
│ └─○ the first tree                                    onward-office   12 msgs
│   ├─▸ first child                                     pushup-upright  3 msgs
│   └─○ second child which has a very long mantra in..  senator-copier  7 msgs
│     └─○ grandchild                                    tallness-hefty  1 msgs
└─○ aria username-prune                                 username-prune  0 msgs
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderFixture(t, tc.width, tc.global); got != tc.want {
				t.Fatalf("rendered list moved:\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
		})
	}
}
