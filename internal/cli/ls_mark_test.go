package cli

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// The docs promise ▸ on your own row while its turn runs, and ls drew ● for
// the aria this shell attends whatever it was doing. Running outranks here:
// the header already names the attended aria.
func TestLsMarksARunningAriaRunningEvenWhenAttended(t *testing.T) {
	for _, c := range []struct {
		state string
		here  bool
		want  string
	}{
		{"active", true, "▸"},
		{"active", false, "▸"},
		{"idle", true, "●"},
		{"dormant", true, "●"},
		{"idle", false, "○"},
	} {
		if got := lsMark(rpc.FigaroInfoResponse{State: c.state}, c.here); got != c.want {
			t.Errorf("state %s here=%v: %s, want %s", c.state, c.here, got, c.want)
		}
	}
}
