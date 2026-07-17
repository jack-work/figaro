package provider

import (
	"encoding/json"
	"sort"
	"text/template"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// Registration describes everything the framework needs to wire a
// provider without provider-specific switch cases in the CLI.
type Registration struct {
	Name         string
	DefaultModel string
	EnvVar       string
	// EnvVars lists accepted environment variables in priority order.
	// When empty, EnvVar is used as the sole source.
	EnvVars   []string
	HasOAuth  bool
	LoginHint string

	// Setup drives the first-run credential acquisition (wizard).
	// Set by the CLI at startup (avoids circular imports with TUI/hush).
	Setup func(loaded *config.Loaded) error

	// Login is called by `figaro login <name>`. Usually same as Setup.
	Login func(loaded *config.Loaded) error

	// Build constructs the provider from an already-assembled context.
	// Provider-specific config (enterprise domain, SDK mode, etc.) is
	// read inside this function from BuildContext.Loaded.
	Build func(BuildContext) (Provider, error)
}

// BuildContext carries everything a provider factory needs. The CLI
// assembles this; the provider package just consumes it.
type BuildContext struct {
	Loaded    *config.Loaded
	Knobs     Knobs
	Resolver  auth.TokenResolver
	Templates *template.Template
	CacheOpen func(aria string) (store.Log[[]json.RawMessage], error)
	Backend   store.Backend
}

var registry = map[string]*Registration{}

func Register(r *Registration) { registry[r.Name] = r }

func Lookup(name string) *Registration { return registry[name] }

func Names() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
