package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/sandbox/agent"
)

type SandboxConfig struct {
	Boxes map[string]BoxConfig  `toml:"boxes"`
	Nodes map[string]NodeConfig `toml:"nodes"`
}

type NodeConfig struct {
	Agent    string   `toml:"agent"`
	Workload []string `toml:"workload"`
}

func (l *Loaded) SandboxNodes() []string { return sortedKeys(l.cfg().Sandbox.Nodes) }

func (l *Loaded) SandboxNode(name string) (NodeConfig, bool) {
	n, ok := l.cfg().Sandbox.Nodes[name]
	return n, ok
}

func (n NodeConfig) Hosts(workload string) bool { return slices.Contains(n.Workload, workload) }

type BoxConfig struct {
	Exec        string   `toml:"exec"`
	Door        string   `toml:"door"`
	Profile     string   `toml:"profile"`
	Providers   []string `toml:"providers"`
	Outfits     []string `toml:"outfits"`
	PurgeGrace  string   `toml:"purge_grace"`
	HostDoor    string   `toml:"host_door"`
	FigaroClaim string   `toml:"figaro_claim"`
}

const FigaroClaimRequired = "required"

func (b BoxConfig) AnyOutfit() bool { return isAnyList(b.Outfits) }

func (b BoxConfig) AnyProvider() bool { return isAnyList(b.Providers) }

func isAnyList(l []string) bool { return len(l) == 1 && l[0] == "*" }

func (l *Loaded) Box(name string) (BoxConfig, bool) {
	if b, ok := l.cfg().Sandbox.Boxes[name]; ok {
		return b, true
	}
	if b, ok := l.leasedBoxes().box(name); ok {
		return b, true
	}
	return l.boundSandboxes().box(name)
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

func (l *Loaded) BoxDoors() map[string]string {
	out := map[string]string{}
	for name, b := range l.cfg().Sandbox.Boxes {
		if b.Door != "" {
			out[name] = filepath.Clean(b.Door)
		}
	}
	return out
}

func (l *Loaded) HostDoors() map[string]string {
	out := map[string]string{}
	for name, b := range l.cfg().Sandbox.Boxes {
		if b.HostDoor != "" {
			out[name] = filepath.Clean(b.HostDoor)
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

func (l *Loaded) DooredBoxes() []string {
	return sortedKeys(l.BoxDoors())
}

func (l *Loaded) BoxDoorWithoutLists(name string) error {
	b, ok := l.cfg().Sandbox.Boxes[name]
	if !ok || b.Door == "" {
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
	return fmt.Errorf("sandbox.boxes.%s has a door but no %s list: the door stays shut, since a box behind it could mint undressed or on any provider; name them, or write [\"*\"] to allow any", name, strings.Join(missing, " or "))
}

func (c Config) validateSandboxNodes() error {
	agents := map[string]string{}
	for _, name := range sortedKeys(c.Sandbox.Nodes) {
		n := c.Sandbox.Nodes[name]
		where := "sandbox.nodes." + name
		if !agent.ValidName(name) {
			return fmt.Errorf("%s: a node name is lowercase letters, digits and dashes, starting with a letter", where)
		}
		path, err := agent.SocketPath(n.Agent)
		if err != nil {
			return fmt.Errorf("%s.agent: %w", where, err)
		}
		if other, ok := agents[path]; ok {
			return fmt.Errorf("%s.agent: %s is also node %s's agent", where, path, other)
		}
		agents[path] = name
		if len(n.Workload) == 0 {
			return fmt.Errorf("%s.workload: name the workloads this node hosts: %q, %q or both", where, agent.Job, agent.Conversation)
		}
		seen := map[string]bool{}
		for _, w := range n.Workload {
			if !agent.ValidWorkload(w) || seen[w] {
				return fmt.Errorf("%s.workload: %q is not %q or %q, or is named twice", where, w, agent.Job, agent.Conversation)
			}
			seen[w] = true
		}
		if _, clash := c.Sandbox.Boxes[name]; clash {
			return fmt.Errorf("%s: a box has that name too", where)
		}
	}
	return nil
}

func (c Config) validateSandbox() error {
	if err := c.validateSandboxNodes(); err != nil {
		return err
	}
	doors := map[string]string{}
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
		case b.Door != "" && !filepath.IsAbs(b.Door):
			return fmt.Errorf("%s.door: want an absolute socket path", where)
		case b.HostDoor != "" && !filepath.IsAbs(b.HostDoor):
			return fmt.Errorf("%s.host_door: want an absolute socket path", where)
		case b.HostDoor != "" && b.Door == "":
			return fmt.Errorf("%s.host_door: a host door reaches the box's door, and this box has none", where)
		case b.FigaroClaim != "" && b.FigaroClaim != FigaroClaimRequired:
			return fmt.Errorf("%s.figaro_claim = %q: want %q, or leave it out", where, b.FigaroClaim, FigaroClaimRequired)
		case b.FigaroClaim != "" && b.Door == "":
			return fmt.Errorf("%s.figaro_claim: the box has no door to require it on", where)
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
		for field, path := range map[string]string{"door": b.Door, "host_door": b.HostDoor} {
			if path == "" {
				continue
			}
			path = filepath.Clean(path)
			if other, ok := doors[path]; ok {
				return fmt.Errorf("%s.%s: %s is also %s", where, field, path, other)
			}
			doors[path] = "sandbox.boxes." + name + "." + field
		}
	}
	for _, name := range sortedKeys(c.Sandbox.Boxes) {
		host := c.Sandbox.Boxes[name].HostDoor
		if host == "" {
			continue
		}
		for _, other := range sortedKeys(c.Sandbox.Boxes) {
			door := c.Sandbox.Boxes[other].Door
			if door == "" {
				continue
			}
			if within(filepath.Dir(filepath.Clean(door)), filepath.Clean(host)) {
				return fmt.Errorf("sandbox.boxes.%s.host_door: %s is inside %s, box %s's door directory, which that box can reach; a host door lives where no box does", name, host, filepath.Dir(filepath.Clean(door)), other)
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
