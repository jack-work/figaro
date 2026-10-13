package meta

import (
	"testing"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

type key string

func (k key) Resolve() (string, error) { return string(k), nil }
func (key) Invalidate(string) error    { return nil }

// Meta's base already ends in /v1, so the default Messages path would post
// to /v1/v1/messages, which Meta answers with an empty body. Checked live:
// /v1/messages, /v1/responses and /v1/chat/completions all serve Wonder.
func TestEndpointsLandOnMetasPaths(t *testing.T) {
	p, err := routed.New(Spec(), provider.Knobs{}, key("k"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for tr, want := range map[routed.Transport]string{
		routed.Messages:  "https://api.meta.ai/v1/messages",
		routed.Responses: "https://api.meta.ai/v1/responses",
		routed.Chat:      "https://api.meta.ai/v1/chat/completions",
	} {
		if got := p.Endpoint(tr); got != want {
			t.Errorf("%s: %s, want %s", tr, got, want)
		}
	}
}
