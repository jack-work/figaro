package provider

import (
	"encoding/json"
	"sort"
	"text/template"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type Registration struct {
	Name         string
	DefaultModel string
	EnvVar       string
	// EnvVars are in priority order; empty means EnvVar alone.
	EnvVars   []string
	HasOAuth  bool
	LoginHint string

	// Setup is set by the CLI at startup (import cycle with TUI/hush).
	Setup func(loaded *config.Loaded) error

	Login func(loaded *config.Loaded) error

	Build func(BuildContext) (Provider, error)

	// ExchangeGrant names the hush grant that derives the bearer from the
	// stored secret; empty means the stored credential is the bearer.
	ExchangeGrant string

	// ExchangeURL returning "" means no exchange; the ordinary credential
	// chain applies.
	ExchangeURL func(*config.Loaded) string
}

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
