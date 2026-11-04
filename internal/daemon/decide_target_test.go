package daemon

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

type headerUpstream struct{}

func (headerUpstream) Endpoint(routed.Transport) string                     { return "" }
func (headerUpstream) Base() string                                         { return "http://upstream.invalid/v1" }
func (headerUpstream) Credential() auth.TokenResolver                       { return nil }
func (headerUpstream) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (headerUpstream) Headers(t routed.Transport) map[string]string {
	h := map[string]string{"x-required-header": "value"}
	if t == routed.Chat {
		h["x-chat-only"] = "yes"
	}
	return h
}

// Fixes #55: the daemon hands decide the provider's headers, chosen for the
// transport the request path names, as the relay does.
func TestDecideTargetCarriesTheProvidersHeaders(t *testing.T) {
	target := decideTarget(headerUpstream{})
	if target.Headers == nil {
		t.Fatal("no headers on the decide target")
	}
	if got := target.Headers("/systemone")["x-required-header"]; got != "value" {
		t.Fatalf("x-required-header = %q", got)
	}
	if got := target.Headers("/chat/completions")["x-chat-only"]; got != "yes" {
		t.Fatalf("transport headers missing for chat: %q", got)
	}
}
