package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestPagerGlobalTopologyMountAndUnsupportedDaemon(t *testing.T) {
	for _, more := range []bool{false, true} {
		for _, unsupported := range []bool{false, true} {
			f := newFakeMounts()
			f.hint = ariasLikeHint
			f.forms[rpc.GlobalAriasFormID] = ariasLike()
			if unsupported {
				f.errs[rpc.GlobalAriasFormID] = errors.New("aria @arias-global not found")
			}
			v := &formView{src: things("here.self", `true`, "here.form", `"@arias"`), open: map[string]bool{}, opener: f.open, topology: &pagerTopology{global: true, more: more}, subject: attending("bbb")}
			v.setView(indexHint)
			seekAttended(v, f.open)
			v.seekMarked()
			var text []string
			for _, row := range v.Items(120) {
				text = append(text, row.str())
			}
			if got := opened(f); len(got) != 1 || got[0] != rpc.GlobalAriasFormID {
				t.Fatalf("opened %v", got)
			}
			if unsupported && !strings.Contains(strings.Join(text, "\n"), "ls -g requires @arias-global") {
				t.Fatalf("old daemon did not report unsupported intrinsic: %v", text)
			}
		}
	}
}
