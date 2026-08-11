package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
)

func snapWith(t *testing.T, kv map[string]any) form.Snapshot {
	t.Helper()
	m := map[string]json.RawMessage{}
	for k, v := range kv {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", k, err)
		}
		m[k] = raw
	}
	return form.FromMap(m)
}

func TestResolveCachePolicyDefaultsOn(t *testing.T) {
	p := ResolveCachePolicy(snapWith(t, nil))
	if p.Off() {
		t.Fatal("caching must be ON by default (system.cache_control absent)")
	}
	if p.Type != "ephemeral" || p.TTL != "" {
		t.Fatalf("default policy = %+v, want {ephemeral, 5m-implicit}", p)
	}
}

func TestResolveCachePolicySettings(t *testing.T) {
	cases := []struct {
		setting  string
		wantType string
		wantTTL  string
	}{
		{"", "ephemeral", ""},
		{"ephemeral", "ephemeral", ""},
		{"short", "ephemeral", ""},
		{"5m", "ephemeral", ""},
		{"  5M  ", "ephemeral", ""},
		{"1h", "ephemeral", "1h"},
		{"long", "ephemeral", "1h"},
		{"none", "", ""},
		{"off", "", ""},
		{"FALSE", "", ""},
		{"nonsense", "ephemeral", ""},
	}
	for _, c := range cases {
		got := ResolveCachePolicy(snapWith(t, map[string]any{CacheControlKey: c.setting}))
		if got.Type != c.wantType || got.TTL != c.wantTTL {
			t.Errorf("setting %q -> %+v, want {%q %q}", c.setting, got, c.wantType, c.wantTTL)
		}
	}
}

// A retention must never be written into the type field: {"type":"1h"} is
// rejected by the API. This is the bug that shipped before the policy was
// centralised, so it gets a named test.
func TestCachePolicyNeverPutsTTLInType(t *testing.T) {
	for _, setting := range []string{"1h", "long", "5m", "ephemeral", "60m"} {
		p := ParseCachePolicy(setting)
		if p.Type != "" && p.Type != "ephemeral" {
			t.Fatalf("setting %q produced type %q; only \"ephemeral\" is a legal type", setting, p.Type)
		}
	}
}

func TestCacheMinTokens(t *testing.T) {
	cases := map[string]int{
		"claude-sonnet-4-5":         1024,
		"claude-sonnet-4.6":         1024,
		"claude-opus-4-1":           1024,
		"claude-opus-4-5":           4096,
		"claude-opus-4.8":           4096,
		"claude-haiku-4-5":          4096,
		"claude-haiku-3-5":          2048,
		"anthropic/claude-opus-4-5": 4096,
		"some-unknown-model":        DefaultCacheMinTokens,
	}
	for model, want := range cases {
		if got := CacheMinTokens(model); got != want {
			t.Errorf("CacheMinTokens(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestEligibleForCache(t *testing.T) {
	if !EligibleForCache("claude-opus-4-5", -1) {
		t.Error("unknown size must mark optimistically")
	}
	if EligibleForCache("claude-opus-4-5", 4095) {
		t.Error("below a 4096 minimum must not spend a breakpoint")
	}
	if !EligibleForCache("claude-opus-4-5", 4096) {
		t.Error("at the minimum must be eligible")
	}
	if !EligibleForCache("claude-sonnet-4-5", 1024) {
		t.Error("sonnet at 1024 must be eligible")
	}
}

func TestBreakpointBudget(t *testing.T) {
	if AutoCacheBreakpoints >= MaxCacheBreakpoints {
		t.Fatalf("figaro must leave a slot for a downstream gateway: %d of %d",
			AutoCacheBreakpoints, MaxCacheBreakpoints)
	}
}

// FuzzParseCachePolicy pins the invariants every marking path relies on:
// the type is always empty or exactly "ephemeral", the TTL is always empty
// or exactly "1h", parsing is idempotent, and no input ever produces a
// TTL on a disabled policy.
func FuzzParseCachePolicy(f *testing.F) {
	for _, seed := range []string{"", "ephemeral", "none", "1h", "5m", "LONG", " off ", "type", "{}", "ttl=1h"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, setting string) {
		p := ParseCachePolicy(setting)
		if p.Type != "" && p.Type != "ephemeral" {
			t.Fatalf("setting %q produced illegal type %q", setting, p.Type)
		}
		if p.TTL != "" && p.TTL != "1h" {
			t.Fatalf("setting %q produced illegal ttl %q", setting, p.TTL)
		}
		if p.Off() && p.TTL != "" {
			t.Fatalf("setting %q disabled caching but kept ttl %q", setting, p.TTL)
		}
		// Idempotence: re-parsing the canonical spelling is a fixed point.
		// "none" is the canonical spelling of the off policy: the empty
		// string means "unset", which is ON by default.
		canonical := "none"
		if !p.Off() {
			canonical = p.Type
			if p.TTL != "" {
				canonical = p.TTL
			}
		}
		if again := ParseCachePolicy(canonical); again != p {
			t.Fatalf("setting %q: %+v not a fixed point (re-parsed %q -> %+v)",
				setting, p, canonical, again)
		}
		if strings.TrimSpace(setting) == "" && p.Off() {
			t.Fatalf("empty setting must keep caching on, got %+v", p)
		}
	})
}
