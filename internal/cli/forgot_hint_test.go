package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// A caller the daemon says may not listen is not told to listen; an old
// daemon that names no verbs, and a listener, still are.
func TestTheForgotHintOffersListenOnlyToAListener(t *testing.T) {
	const id = "417279274b960ad202a4983c645b4fba"
	cases := []struct {
		name  string
		grant rpc.AttachResponse
		want  bool
	}{
		{"send only", rpc.AttachResponse{May: []rpc.Verb{rpc.VerbSend}}, false},
		{"listener", rpc.AttachResponse{May: []rpc.Verb{rpc.VerbListen, rpc.VerbSend}}, true},
		{"daemon names no verbs", rpc.AttachResponse{}, true},
	}
	for _, c := range cases {
		if got := strings.Contains(forgotHint(id, c.grant), "figaro listen"); got != c.want {
			t.Errorf("%s: hint %q, offers listen = %v, want %v", c.name, forgotHint(id, c.grant), got, c.want)
		}
	}
}
