package tool

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	sandbox string
}

func (r *Registry) Sandbox() string { return r.sandbox }

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("cannot register nil tool")
	}
	name := t.Name()
	if name == "" {
		return fmt.Errorf("cannot register tool with empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

func (r *Registry) MustRegister(tools ...Tool) {
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

type RegistryOption func(*registryOpts)

type registryOpts struct {
	imageLimits ImageLimits
}

func WithImageBudget(maxBase64 int) RegistryOption {
	return func(o *registryOpts) {
		if maxBase64 > 0 {
			o.imageLimits.MaxBase64 = maxBase64
		}
	}
}

func DefaultRegistry(box sandbox.Sandbox, cwdFn func() string, opts ...RegistryOption) *Registry {
	settings := registryOpts{imageLimits: DefaultImageLimits()}
	for _, opt := range opts {
		opt(&settings)
	}
	r := NewRegistry()
	r.sandbox = box.Isolation().Provider
	r.MustRegister(
		&BashTool{Box: box, CwdFn: cwdFn},
		&ProcessTool{Sessions: box.Sessions()},
		&ReadTool{Box: box, CwdFn: cwdFn, ImageLimits: settings.imageLimits},
		&WriteTool{Box: box, CwdFn: cwdFn},
		&EditTool{Box: box, CwdFn: cwdFn},
	)
	return r
}

func cwdOf(fn func() string) string {
	if fn == nil {
		return ""
	}
	return fn()
}

func resolve(cwdFn func() string, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(cwdOf(cwdFn), path)
}

type SessionRegistry = local.Host

func NewSessionRegistry(ttl time.Duration) *SessionRegistry { return local.NewHost(ttl) }

const DefaultSessionTTL = local.DefaultTTL
