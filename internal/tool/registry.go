package tool

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is a name-keyed collection of Tools. The agent injects one
// at startup and looks tools up by name when dispatching tool calls.
// Safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds t to the registry. Returns an error if a tool with the
// same name is already registered.
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

// MustRegister adds one or more tools to the registry. Panics on error.
// Convenience for static startup wiring.
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

// List returns all registered tools in stable alphabetical order by name.
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

// Names returns all registered tool names in stable alphabetical order.
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

// DefaultRegistry returns a Registry pre-populated with the standard
// coding tools (bash, read, write, edit), all bound to cwd. Intended
// as a one-liner for both the agent and external Go programs that
// want the same toolset.
func DefaultRegistry(cwd string) *Registry {
	r := NewRegistry()
	r.MustRegister(
		NewBashTool(cwd),
		NewReadTool(cwd),
		NewWriteTool(cwd),
		NewEditTool(cwd),
	)
	return r
}
