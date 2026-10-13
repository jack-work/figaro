package routed

import (
	"testing"

	"github.com/jack-work/figaro/internal/provider"
)

// The relay posts to these endpoints directly, so they must agree with the
// paths figaro's own transports use for the same spec.
func TestRelayEndpointsAndHeadersFollowTheSpec(t *testing.T) {
	spec := Spec{
		Name:             "x",
		BaseURL:          func() string { return "https://api.example/v1/" },
		Default:          Responses,
		Headers:          map[string]string{"X-Static": "s"},
		TransportHeaders: map[Transport]map[string]string{Messages: {"X-Messages": "m"}},
		MessagesPath:     "/messages",
	}
	p, err := New(spec, provider.Knobs{}, staticToken("k"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for tr, want := range map[Transport]string{
		Responses: "https://api.example/v1/responses",
		Chat:      "https://api.example/v1/chat/completions",
		Messages:  "https://api.example/v1/messages",
	} {
		if got := p.Endpoint(tr); got != want {
			t.Errorf("%s: %s, want %s", tr, got, want)
		}
	}
	if h := p.Headers(Messages); h["X-Static"] != "s" || h["X-Messages"] != "m" {
		t.Errorf("messages headers %v", h)
	}
	if h := p.Headers(Chat); h["X-Messages"] != "" {
		t.Errorf("a messages-only header reached chat: %v", h)
	}
	if tok, _ := p.Credential().Resolve(); tok != "k" {
		t.Errorf("credential %q", tok)
	}
}

func TestTransportForPath(t *testing.T) {
	for path, want := range map[string]Transport{
		"/chat/completions":    Chat,
		"/v1/chat/completions": Chat,
		"/v1/messages":         Messages,
		"/responses/":          Responses,
		"/models":              "",
		"/responses/abc":       "",
	} {
		if got := TransportForPath(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
