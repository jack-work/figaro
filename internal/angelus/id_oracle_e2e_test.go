package angelus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

// El Inspector XVI, D6: a principal no grant lets name a figaro learns
// nothing about which ids exist. Every route answers an id the owner holds
// and an id nobody holds with the same code and words, and decide refuses
// by policy before it looks the id up.
func TestAPrincipalThatNamesNoFigaroCannotTellWhichIDsExist(t *testing.T) {
	r := svcEndpoint(t, "decide:outfit:narrow-decide")
	c := r.client()
	missing := strings.Repeat("ab", 16)
	refusal := func(m, id string) string {
		reply := c.call(m, onAria(id, map[string]any{"text": "witness", "question": "go?", "version": rpc.ProtocolVersion, "outfit": "narrow-decide"}))
		if reply.Error == nil {
			return "answered: " + strings.ReplaceAll(string(reply.Result), id, "<id>")
		}
		return fmt.Sprintf("%d %s", reply.Error.Code, strings.ReplaceAll(reply.Error.Message, id, "<id>"))
	}
	pairs := map[string][2]string{
		"full id":      {r.owned, missing},
		"prefix":       {r.owned[:8], missing[:8]},
		"short prefix": {r.owned[:4], missing[:4]},
		"handle":       {handle.Words(r.owned, handle.MinWords), handle.Words(missing, handle.MinWords)},
		"long handle":  {handle.Words(r.owned, handle.MaxWords), handle.Words(missing, handle.MaxWords)},
	}
	for name, ids := range pairs {
		for _, m := range r.served {
			if m == rpc.MethodHello {
				continue
			}
			held, absent := refusal(m, ids[0]), refusal(m, ids[1])
			if held != absent {
				t.Errorf("%s: %s tells the ids apart:\n  held:    %s\n  missing: %s", name, m, held, absent)
			}
		}
	}
	if got := refusal(rpc.MethodDecide, missing); !strings.HasPrefix(got, "-32020 ") || !strings.Contains(got, "holds no grant that names a figaro") {
		t.Fatalf("decide on a missing id: %s; want the policy's refusal, given before the id is looked up", got)
	}
}
