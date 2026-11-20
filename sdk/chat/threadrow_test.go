package chat

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

func TestARowReadsAsLiveDormantOrRetired(t *testing.T) {
	cases := []struct {
		name  string
		row   rpc.FigaroInfoResponse
		state State
		busy  bool
	}{
		{"idle", rpc.FigaroInfoResponse{State: "idle"}, StateLive, false},
		{"active", rpc.FigaroInfoResponse{State: "active"}, StateLive, true},
		{"suspended", rpc.FigaroInfoResponse{State: "suspended"}, StateLive, false},
		{"dormant", rpc.FigaroInfoResponse{State: "dormant"}, StateDormant, false},
		{"retired and asleep", rpc.FigaroInfoResponse{State: "dormant", RetiredAt: 5}, StateRetired, false},
		{"retired and awake", rpc.FigaroInfoResponse{State: "idle", RetiredAt: 5}, StateRetired, false},
	}
	for _, c := range cases {
		got := threadOf(c.row)
		if got.State != c.state || got.Busy != c.busy {
			t.Errorf("%s: state %q busy %v, want %q %v", c.name, got.State, got.Busy, c.state, c.busy)
		}
	}
}

func TestARowWithNoTurnHasNoTime(t *testing.T) {
	if got := threadOf(rpc.FigaroInfoResponse{LastActive: 1000}); !got.At.IsZero() {
		t.Fatalf("no turn but time %v", got.At)
	}
	if got := threadOf(rpc.FigaroInfoResponse{LastActive: 1000, LastTurn: 2}); !got.At.Equal(time.UnixMilli(1000)) {
		t.Fatalf("time %v", got.At)
	}
}

func TestAParentThatIsAnOutfitIsNoParent(t *testing.T) {
	if got := threadOf(rpc.FigaroInfoResponse{Parent: "@abc", Vector: []int{0}}); got.Parent != "" || got.Depth != 0 {
		t.Fatalf("%+v", got)
	}
	if got := threadOf(rpc.FigaroInfoResponse{Parent: "abc", Vector: []int{0, 1}}); got.Parent != "abc" || got.Depth != 1 {
		t.Fatalf("%+v", got)
	}
}
