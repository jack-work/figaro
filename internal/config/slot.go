package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type SlotConfig struct {
	Pools map[string]PoolConfig `toml:"pools"`
}

type PoolConfig struct {
	Enabled           bool       `toml:"enabled"`
	Outfit            string     `toml:"outfit"`
	Provider          string     `toml:"provider"`
	Rou2LeaseClass    string     `toml:"rou2_lease_class"`
	SayURL            string     `toml:"say_url"`
	SuccessionsPerDay *int       `toml:"successions_per_day"`
	SearchCredential  string     `toml:"search_credential"`
	DeliverMaxBytes   int        `toml:"deliver_max_bytes"`
	Slots             []PoolSlot `toml:"slots"`
}

type PoolSlot string

func (s *PoolSlot) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case string:
		*s = PoolSlot(v)
		return nil
	case map[string]any:
		return fmt.Errorf("a slot is a name and nothing else: this one is a table carrying %s", strings.Join(sortedKeys(v), ", "))
	}
	return fmt.Errorf("a slot is a name; this one is of type %T", v)
}

const (
	DefaultSuccessionsPerDay = 4
	DefaultDeliverMaxBytes   = 16 << 10

	reservedOutfit = "default"
)

func (p PoolConfig) SuccessionBound() int {
	if p.SuccessionsPerDay == nil {
		return DefaultSuccessionsPerDay
	}
	return *p.SuccessionsPerDay
}

func (p PoolConfig) DeliverCap() int {
	if p.DeliverMaxBytes == 0 {
		return DefaultDeliverMaxBytes
	}
	return p.DeliverMaxBytes
}

func (p PoolConfig) SlotNames() []string {
	out := make([]string, len(p.Slots))
	for i, s := range p.Slots {
		out[i] = string(s)
	}
	return out
}

func (l *Loaded) PoolDaemon() bool { return len(l.cfg().Slot.Pools) > 0 }

func (l *Loaded) Pools() []string { return sortedKeys(l.cfg().Slot.Pools) }

func (l *Loaded) Pool(name string) (PoolConfig, bool) {
	p, ok := l.cfg().Slot.Pools[name]
	return p, ok
}

var leaseSuffix = regexp.MustCompile(`-g[0-9]+$`)

func nameHasSeparator(name string) bool {
	return name == "" || strings.ContainsAny(name, ":/@* \t")
}

func refuseUnknownSlotKeys(md toml.MetaData) error {
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "slot" {
			return fmt.Errorf("%s: not a key figaro knows; a pool reads no key it does not know, so a misspelt one cannot leave it open", k)
		}
	}
	return nil
}

func (c Config) validateSlot() error {
	owner := map[string]string{}
	for _, name := range sortedKeys(c.Slot.Pools) {
		p := c.Slot.Pools[name]
		where := "slot.pools." + name
		switch {
		case nameHasSeparator(name):
			return fmt.Errorf("%s: a pool name has no : / @ * or space", where)
		case p.Outfit == "":
			return fmt.Errorf("%s.outfit: a pool names the one outfit its figaros wear", where)
		case p.Outfit == reservedOutfit || p.Outfit == c.DefaultOutfit:
			return fmt.Errorf("%s.outfit = %q: that is the outfit of the daemon's own figaros; a pool has its own", where, p.Outfit)
		case p.Provider == "":
			return fmt.Errorf("%s.provider: a pool names the one provider its figaros use", where)
		case p.Rou2LeaseClass == "":
			return fmt.Errorf("%s.rou2_lease_class: a lease spends under a class of caps, and none is named", where)
		case p.SuccessionsPerDay != nil && *p.SuccessionsPerDay < 0:
			return fmt.Errorf("%s.successions_per_day %d is negative", where, *p.SuccessionsPerDay)
		case p.DeliverMaxBytes < 0:
			return fmt.Errorf("%s.deliver_max_bytes %d is negative", where, p.DeliverMaxBytes)
		case p.SearchCredential != "" && filepath.Base(p.SearchCredential) != p.SearchCredential:
			return fmt.Errorf("%s.search_credential %q: want the name of a file in the unit's credentials directory, not a path", where, p.SearchCredential)
		case p.SearchCredential == "." || p.SearchCredential == "..":
			return fmt.Errorf("%s.search_credential %q: want the name of a file", where, p.SearchCredential)
		case len(p.Slots) == 0:
			return fmt.Errorf("%s.slots: a pool with no slot can lease nothing", where)
		}
		if u, err := url.Parse(p.SayURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("%s.say_url %q: want an http or https URL with a host and no credentials", where, p.SayURL)
		}
		for _, slot := range p.SlotNames() {
			switch {
			case nameHasSeparator(slot):
				return fmt.Errorf("%s.slots: %q: a slot name has no : / @ * or space", where, slot)
			case slot == "local" || slot == "bwrap" || slot == "self":
				return fmt.Errorf("%s.slots: %s is reserved", where, slot)
			case leaseSuffix.MatchString(slot):
				return fmt.Errorf("%s.slots: %q ends like a lease (<slot>-g<generation>); a slot name does not", where, slot)
			}
			if other, taken := owner[slot]; taken {
				return fmt.Errorf("%s.slots: %q is also a slot of %s; a lease is named after its slot alone", where, slot, other)
			}
			owner[slot] = where
		}
	}
	return nil
}

func (c Config) refuseWhatAPoolDaemonCannotHold(md toml.MetaData, providerFiles []string) error {
	if len(c.Slot.Pools) == 0 {
		return nil
	}
	for _, table := range [][]string{{"troupe"}, {"peers"}, {"door", "tls"}, {"sandbox", "boxes"}, {"service_tokens"}, {"authz", "api_keys"}} {
		if md.IsDefined(table...) {
			return fmt.Errorf("[%s] beside [slot.pools]: a daemon that leases slots holds no troupe, peer, node door, static box, service token or api key", strings.Join(table, "."))
		}
	}
	if c.Trunks != nil && !*c.Trunks {
		return fmt.Errorf("trunks = false beside [slot.pools]: a lease's figaro and role are placed under their pool's node, and a store without trunks has no tree to place them in")
	}
	if md.IsDefined("authz", "peercred") {
		return fmt.Errorf("[authz.peercred] beside [slot.pools]: a daemon that leases slots maps no uid to a principal; its own uid is the owner and no other comes in by its socket")
	}
	if strings.EqualFold(strings.TrimSpace(c.Telemetry.Level), "debug") {
		return fmt.Errorf("[telemetry] level = %q beside [slot.pools]: debug logs carry prompt text, and a pool's logs carry ids, lengths and counts only", c.Telemetry.Level)
	}
	if c.Authz.Policy == "" {
		return fmt.Errorf("[slot.pools] without [authz] policy: a daemon that leases slots names its policy file and never falls back to the default policy")
	}
	var used []string
	for _, p := range c.Slot.Pools {
		used = append(used, p.Provider)
	}
	sort.Strings(providerFiles)
	for _, name := range providerFiles {
		if !slices.Contains(used, name) {
			return fmt.Errorf("providers/%s.toml beside [slot.pools]: no pool uses %s, and a daemon that leases slots holds no other provider's credential", name, name)
		}
	}
	return nil
}
