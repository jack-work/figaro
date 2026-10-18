package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// A row from another node is shown by the name that reaches it from here:
// its node's short id, then its own.
func TestRemoteRowsShowTheirNode(t *testing.T) {
	local, remote := "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	plain := rpc.ShortIDs([]string{local, remote})
	short := namesOf([]rpc.FigaroInfoResponse{
		{ID: local},
		{ID: remote, Node: "65cb28d9-e752-4627-bbdc-44fb3750dbde"},
	})
	if got := short[local]; got != plain[local] {
		t.Fatalf("local row: %q, want %q", got, plain[local])
	}
	if got, want := short[remote], "65cb28d9/"+plain[remote]; got != want {
		t.Fatalf("remote row: %q, want %q", got, want)
	}
	named := namesOf([]rpc.FigaroInfoResponse{
		{ID: local, Handle: "nursery-lioness", Short: "01234"},
		{ID: remote, Handle: "suds-obtain", Short: "fedcb", Node: "65cb28d9-e752-4627-bbdc-44fb3750dbde"},
	})
	if named[local] != "nursery-lioness" || named[remote] != "65cb28d9/suds-obtain" {
		t.Fatalf("rows the daemon named show their handles: %v", named)
	}
}

// Tree vectors are positions in one store; a remote aria at [1, 1] is the
// child of its own node's [1], never of the local [1].
func TestRemoteRowsNestUnderTheirOwnNode(t *testing.T) {
	const node = "65cb28d9-e752-4627-bbdc-44fb3750dbde"
	figs := []rpc.FigaroInfoResponse{
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Vector: []int{1}},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Vector: []int{1}, Node: node},
		{ID: "cccccccccccccccccccccccccccccccc", Vector: []int{1, 1}, Node: node},
	}
	tree, _ := listTree(figs, "", 0)
	if len(tree.Roots) != 2 {
		t.Fatalf("%d roots, want 2", len(tree.Roots))
	}
	for _, r := range tree.Roots {
		remote := len(r.Children) == 1
		if strings.Contains(r.Label, "aaaaa") && remote {
			t.Fatal("the remote child nested under the local root")
		}
		if strings.Contains(r.Label, "bbbbb") && !remote {
			t.Fatal("the remote child lost its parent")
		}
	}
}
