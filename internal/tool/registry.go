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

// DefaultRegistry returns a registry with bash, read, write, edit.
//
// The bash tool gets a LocalExecutor with the default daemon-env
// sanitizer wired in, so child processes don't inherit
// _FIGARO_DAEMON / HUSH_* and silently re-enter daemon mode.
func DefaultRegistry(cwd string) *Registry {
	return DefaultRegistryFn(func() string { return cwd })
}

// DefaultRegistryFn is like DefaultRegistry but reads cwd at call time
// via cwdFn. Agent wiring should pass a closure that pulls system.cwd
// from the chalkboard.
func DefaultRegistryFn(cwdFn func() string) *Registry {
	r := NewRegistry()
	executor := NewLocalExecutor(
		NewDefaultEnvSanitizer(),
		CwdResolver{Fn: cwdFn},
	)
	staticCwd := ""
	if cwdFn != nil {
		staticCwd = cwdFn()
	}
	r.MustRegister(
		NewBashToolWith(cwdFn, executor),
		NewReadTool(staticCwd),
		NewWriteTool(staticCwd),
		NewEditTool(staticCwd),
	)
	return r
}
