package provider

import (
	"strings"
	"testing"
)

func TestRouteURLs(t *testing.T) {
	direct := AnthropicDirect()
	if got, want := direct.MessagesURL(), "https://api.anthropic.com/v1/messages"; got != want {
		t.Errorf("anthropic messages URL = %q, want %q", got, want)
	}
	or := OpenRouterRoute()
	if got, want := or.MessagesURL(), "https://openrouter.ai/api/v1/chat/completions"; got != want {
		t.Errorf("openrouter messages URL = %q, want %q", got, want)
	}
	gw := GatewayRoute("http://127.0.0.1:61890/v1/")
	if got, want := gw.MessagesURL(), "http://127.0.0.1:61890/v1/chat/completions"; got != want {
		t.Errorf("gateway messages URL = %q, want %q", got, want)
	}
}

func TestQualifyModel(t *testing.T) {
	or := OpenRouterRoute()
	if got, want := or.QualifyModel("claude-sonnet-4-6"), "anthropic/claude-sonnet-4-6"; got != want {
		t.Errorf("QualifyModel = %q, want %q", got, want)
	}
	if got, want := or.QualifyModel("openai/gpt-5.6"), "openai/gpt-5.6"; got != want {
		t.Errorf("already-qualified model was re-prefixed: %q", got)
	}
	if got, want := AnthropicDirect().QualifyModel("claude-sonnet-4-6"), "claude-sonnet-4-6"; got != want {
		t.Errorf("direct route must not namespace models: %q, want %q", got, want)
	}
}

func TestBaseURLOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.example/v1/")
	r := WithBaseURLOverride(AnthropicDirect(), "anthropic")
	if got, want := r.BaseURL, "https://proxy.example/v1"; got != want {
		t.Errorf("override BaseURL = %q, want %q (trailing slash trimmed)", got, want)
	}
	if got, want := r.MessagesURL(), "https://proxy.example/v1/messages"; got != want {
		t.Errorf("override messages URL = %q, want %q", got, want)
	}
}

func TestBaseURLOverrideUnsetLeavesRouteAlone(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "")
	if got, want := WithBaseURLOverride(AnthropicDirect(), "anthropic").BaseURL,
		AnthropicDirect().BaseURL; got != want {
		t.Errorf("empty override changed the route: %q", got)
	}
}

// A route that advances the breakpoint itself must not also get a moving
// per-block marker. Marking a message on turn N and un-marking it on turn
// N+1 rewrites bytes that are already inside the cached prefix, which
// invalidates the cache the marker exists to fill.
func TestMarkTailIsSuppressedWhenGatewayAdvancesIt(t *testing.T) {
	if !GatewayRoute("http://x/v1").Caps.MarkTail() {
		t.Error("a gateway with no top-level directive must take a per-block tail marker")
	}
	if OpenRouterRoute().Caps.MarkTail() {
		t.Error("OpenRouter advances the breakpoint itself; a moving block marker would churn prefix bytes")
	}
	if UncachedRoute("http://x/v1").Caps.MarkTail() {
		t.Error("an unknown gateway must never be marked")
	}
}

func TestUncachedRouteMarksNothing(t *testing.T) {
	caps := UncachedRoute("http://x/v1").Caps
	if caps.BlockMarkers || caps.TopLevel || caps.TTL || caps.SessionKey || caps.MaxMarkers != 0 {
		t.Fatalf("UncachedRoute advertises a capability: %+v", caps)
	}
}

func TestGatewayRouteStaysUnderTheFigaroBudget(t *testing.T) {
	if got := GatewayRoute("http://x/v1").Caps.MaxMarkers; got != AutoCacheBreakpoints {
		t.Errorf("gateway MaxMarkers = %d, want %d so the router can top up to %d",
			got, AutoCacheBreakpoints, MaxCacheBreakpoints)
	}
}

func TestSessionKeyIsStableOpaqueAndBounded(t *testing.T) {
	const aria = "30bc6333"
	first := SessionKey(aria)
	if first != SessionKey(aria) {
		t.Fatal("session key must be stable across turns")
	}
	if first == SessionKey("30bc6334") {
		t.Fatal("distinct arias must not collide")
	}
	if strings.Contains(first, aria) {
		t.Fatalf("session key %q leaks the aria id", first)
	}
	if len(first) > sessionKeyMaxLen {
		t.Fatalf("session key is %d chars, over the %d ceiling", len(first), sessionKeyMaxLen)
	}
	if SessionKey("") != "" {
		t.Fatal("no aria id means no session key")
	}
}
