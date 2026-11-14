package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type SandboxConfig struct {
	Boxes map[string]BoxConfig `toml:"boxes"`
}

type BoxConfig struct {
	Exec         string   `toml:"exec"`
	Endpoint     string   `toml:"endpoint"`
	Profile      string   `toml:"profile"`
	Providers    []string `toml:"providers"`
	Outfits      []string `toml:"outfits"`
	PurgeGrace   string   `toml:"purge_grace"`
	HostEndpoint string   `toml:"host_endpoint"`
	FigaroClaim  string   `toml:"figaro_claim"`
}

const FigaroClaimRequired = "required"

func (b BoxConfig) AnyOutfit() bool { return isAnyList(b.Outfits) }

func (b BoxConfig) AnyProvider() bool { return isAnyList(b.Providers) }

func isAnyList(l []string) bool { return len(l) == 1 && l[0] == "*" }

func (l *Loaded) Box(name string) (BoxConfig, bool) {
	if b, ok := l.cfg().Sandbox.Boxes[name]; ok {
		return b, true
	}
	return l.leasedBoxes().box(name)
}

func (l *Loaded) BoxPurgeGrace(name string) (time.Duration, bool) {
	b, ok := l.Box(name)
	if !ok || b.PurgeGrace == "" {
		return 0, false
	}
	d, err := time.ParseDuration(b.PurgeGrace)
	if err != nil {
		return 0, false
	}
	return d, true
}

func (l *Loaded) ShortestBoxPurgeGrace() (time.Duration, bool) {
	var least time.Duration
	found := false
	for _, name := range append(sortedKeys(l.cfg().Sandbox.Boxes), l.LeasedBoxes()...) {
		if d, ok := l.BoxPurgeGrace(name); ok && (!found || d < least) {
			least, found = d, true
		}
	}
	return least, found
}

func (l *Loaded) BoxEndpoints() map[string]string {
	out := map[string]string{}
	for name, b := range l.cfg().Sandbox.Boxes {
		if b.Endpoint != "" {
			out[name] = filepath.Clean(b.Endpoint)
		}
	}
	return out
}

func (l *Loaded) HostEndpoints() map[string]string {
	out := map[string]string{}
	for name, b := range l.cfg().Sandbox.Boxes {
		if b.HostEndpoint != "" {
			out[name] = filepath.Clean(b.HostEndpoint)
		}
	}
	return out
}

func (l *Loaded) BoxesRequiringClaim() []string {
	var out []string
	for _, name := range sortedKeys(l.cfg().Sandbox.Boxes) {
		if l.cfg().Sandbox.Boxes[name].FigaroClaim == FigaroClaimRequired {
			out = append(out, name)
		}
	}
	return out
}

func (l *Loaded) BoxesWithEndpoints() []string {
	return sortedKeys(l.BoxEndpoints())
}

func (l *Loaded) BoxEndpointWithoutLists(name string) error {
	b, ok := l.cfg().Sandbox.Boxes[name]
	if !ok || b.Endpoint == "" {
		return nil
	}
	var missing []string
	if len(b.Outfits) == 0 {
		missing = append(missing, "outfits")
	}
	if len(b.Providers) == 0 {
		missing = append(missing, "providers")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("sandbox.boxes.%s has an endpoint but no %s list: the endpoint stays shut, since a box behind it could mint undressed or on any provider; name them, or write [\"*\"] to allow any", name, strings.Join(missing, " or "))
}

func (c Config) validateSandbox() error {
	endpoints := map[string]string{}
	for _, name := range sortedKeys(c.Sandbox.Boxes) {
		b := c.Sandbox.Boxes[name]
		where := "sandbox.boxes." + name
		switch {
		case name == "local" || name == "bwrap" || name == "self":
			return fmt.Errorf("%s: %s is reserved", where, name)
		case name == "" || strings.ContainsAny(name, ":/@* \t"):
			return fmt.Errorf("%s: a box name has no : / @ * or space", where)
		case !filepath.IsAbs(b.Exec):
			return fmt.Errorf("%s.exec: want the absolute path of the socket box serve listens on", where)
		case b.Endpoint != "" && !filepath.IsAbs(b.Endpoint):
			return fmt.Errorf("%s.endpoint: want an absolute socket path", where)
		case b.HostEndpoint != "" && !filepath.IsAbs(b.HostEndpoint):
			return fmt.Errorf("%s.host_endpoint: want an absolute socket path", where)
		case b.HostEndpoint != "" && b.Endpoint == "":
			return fmt.Errorf("%s.host_endpoint: a host endpoint reaches the box's endpoint, and this box has none", where)
		case b.FigaroClaim != "" && b.FigaroClaim != FigaroClaimRequired:
			return fmt.Errorf("%s.figaro_claim = %q: want %q, or leave it out", where, b.FigaroClaim, FigaroClaimRequired)
		case b.FigaroClaim != "" && b.Endpoint == "":
			return fmt.Errorf("%s.figaro_claim: the box has no endpoint to require it on", where)
		}
		for field, list := range map[string][]string{"outfits": b.Outfits, "providers": b.Providers} {
			if len(list) > 1 && slices.Contains(list, "*") {
				return fmt.Errorf("%s.%s: \"*\" means any and stands alone, not beside names", where, field)
			}
		}
		if b.PurgeGrace != "" {
			if d, err := time.ParseDuration(b.PurgeGrace); err != nil || d < 0 {
				return fmt.Errorf("%s.purge_grace %q: want a duration like \"1h\"", where, b.PurgeGrace)
			}
		}
		for field, path := range map[string]string{"endpoint": b.Endpoint, "host_endpoint": b.HostEndpoint} {
			if path == "" {
				continue
			}
			path = filepath.Clean(path)
			if other, ok := endpoints[path]; ok {
				return fmt.Errorf("%s.%s: %s is also %s", where, field, path, other)
			}
			endpoints[path] = "sandbox.boxes." + name + "." + field
		}
	}
	for _, name := range sortedKeys(c.Sandbox.Boxes) {
		host := c.Sandbox.Boxes[name].HostEndpoint
		if host == "" {
			continue
		}
		for _, other := range sortedKeys(c.Sandbox.Boxes) {
			endpoint := c.Sandbox.Boxes[other].Endpoint
			if endpoint == "" {
				continue
			}
			if within(filepath.Dir(filepath.Clean(endpoint)), filepath.Clean(host)) {
				return fmt.Errorf("sandbox.boxes.%s.host_endpoint: %s is inside %s, box %s's endpoint directory, which that box can reach; a host endpoint lives where no box does", name, host, filepath.Dir(filepath.Clean(endpoint)), other)
			}
		}
	}
	return nil
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
