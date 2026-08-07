package tool

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is a name-keyed collection of Tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool. Error on duplicate name.
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

// MustRegister adds tools, panicking on error.
func (r *Registry) MustRegister(tools ...Tool) {
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
}

// Get returns the tool registered under name, or nil, false if absent.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all tools sorted by name.
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

// Names returns all tool names sorted.
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

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// RegistryOption tunes the default registry. Variadic so every existing call
// site keeps compiling and only the callers that HAVE a configuration need to
// know one exists.
type RegistryOption func(*registryOpts)

type registryOpts struct {
	imageLimits ImageLimits
	sessions    *SessionRegistry
}

// WithSessions shares a caller-owned SessionRegistry instead of minting a
// private one. The daemon passes one instance for every aria, which is
// what lets a backgrounded job outlive its agent: sessions are keyed by
// scope, so an agent can be torn down and rebuilt while its children keep
// running under ids that stay stable. It also makes seq daemon-global, so
// a wake cannot mint a bg-1 that an orphan still answers to.
func WithSessions(sessions *SessionRegistry) RegistryOption {
	return func(o *registryOpts) {
		if sessions != nil {
			o.sessions = sessions
		}
	}
}

// WithImageBudget caps the base64 payload of one inlined image. The agent
// passes config.InlineImageBudget() so the tool that PRODUCES the image is
// bounded by the same number the store can actually append — an image fitted
// at ingest never has to be dropped downstream.
func WithImageBudget(maxBase64 int) RegistryOption {
	return func(o *registryOpts) {
		if maxBase64 > 0 {
			o.imageLimits.MaxBase64 = maxBase64
		}
	}
}

// DefaultRegistry returns a registry with bash, read, write, edit.
//
// The bash tool gets a LocalExecutor with the default daemon-env
// sanitizer wired in, so child processes don't inherit
// _FIGARO_DAEMON / HUSH_* and silently re-enter daemon mode.
func DefaultRegistry(cwd string, opts ...RegistryOption) *Registry {
	return DefaultRegistryFn(func() string { return cwd }, opts...)
}

// DefaultRegistryFn is like DefaultRegistry but reads cwd at call time
// via cwdFn. Agent wiring should pass a closure that pulls system.cwd
// from the chalkboard.
func DefaultRegistryFn(cwdFn func() string, opts ...RegistryOption) *Registry {
	return DefaultRegistryForAria("", cwdFn, opts...)
}

// DefaultRegistryForAria is DefaultRegistryFn for a named aria: the
// bash tool exports FIGARO_ARIA=<ariaID> to its children, so nested
// `figaro` calls are statically attended to the aria that spawned
// them. Pass "" when there is no aria (tests, one-off registries).
//
// The aria id is also the SESSION SCOPE, which matters once a caller
// passes WithSessions: with a private registry the scope is cosmetic, with
// a shared one it is the isolation boundary.
func DefaultRegistryForAria(ariaID string, cwdFn func() string, opts ...RegistryOption) *Registry {
	settings := registryOpts{imageLimits: DefaultImageLimits()}
	for _, opt := range opts {
		opt(&settings)
	}
	r := NewRegistry()
	executor := NewLocalExecutor(
		NewDefaultEnvSanitizer(),
		CwdResolver{Fn: cwdFn},
	)
	staticCwd := ""
	if cwdFn != nil {
		staticCwd = cwdFn()
	}
	// bash and process share one session registry so backgrounded
	// commands are reachable across both tools. Shared when the caller
	// supplied one (the daemon does), private otherwise.
	sessions := settings.sessions
	if sessions == nil {
		sessions = NewSessionRegistry(DefaultSessionTTL)
	}
	// Both tools must agree on the scope or process cannot see what bash
	// spawned. Empty ariaID falls through to defaultScope.
	var scopeFn func() string
	if ariaID != "" {
		scopeFn = func() string { return ariaID }
	}
	bash := NewBashToolForAria(ariaID, cwdFn, executor, sessions)
	bash.ScopeFn = scopeFn
	r.MustRegister(
		bash,
		NewProcessTool(sessions, scopeFn),
		&ReadTool{Cwd: staticCwd, ImageLimits: settings.imageLimits},
		NewWriteTool(staticCwd),
		NewEditTool(staticCwd),
	)
	return r
}
