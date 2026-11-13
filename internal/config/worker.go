package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type WorkerConfig struct {
	Pools map[string]PoolConfig `toml:"pools"`
}

type PoolConfig struct {
	Enabled           bool         `toml:"enabled"`
	Outfit            string       `toml:"outfit"`
	Provider          string       `toml:"provider"`
	Rou2LeaseClass    string       `toml:"rou2_lease_class"`
	Rou2URL           string       `toml:"rou2_url"`
	SayURL            string       `toml:"say_url"`
	SuccessionsPerDay *int         `toml:"successions_per_day"`
	SayPerMinute      *int         `toml:"say_per_minute"`
	SearchURL         string       `toml:"search_url"`
	SearchCredential  string       `toml:"search_credential"`
	SearchPerMinute   *int         `toml:"search_per_minute"`
	SearchPerDay      *int         `toml:"search_per_day"`
	DeliverMaxBytes   int          `toml:"deliver_max_bytes"`
	Workers           []PoolWorker `toml:"slots"`
}

type PoolWorker string

func (s *PoolWorker) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case string:
		*s = PoolWorker(v)
		return nil
	case map[string]any:
		return fmt.Errorf("a slot is a name and nothing else: this one is a table carrying %s", strings.Join(sortedKeys(v), ", "))
	}
	return fmt.Errorf("a slot is a name; this one is of type %T", v)
}

const (
	DefaultSuccessionsPerDay = 4
	DefaultDeliverMaxBytes   = 16 << 10
	DefaultSayPerMinute      = 20
	DefaultSearchPerMinute   = 6
	DefaultSearchPerDay      = 100

	reservedOutfit = "default"
)

func (p PoolConfig) SuccessionBound() int {
	if p.SuccessionsPerDay == nil {
		return DefaultSuccessionsPerDay
	}
	return *p.SuccessionsPerDay
}

func (p PoolConfig) SayBound() int {
	if p.SayPerMinute == nil {
		return DefaultSayPerMinute
	}
	return *p.SayPerMinute
}

func (p PoolConfig) SearchBounds() (perMinute, perDay int) {
	perMinute, perDay = DefaultSearchPerMinute, DefaultSearchPerDay
	if p.SearchPerMinute != nil {
		perMinute = *p.SearchPerMinute
	}
	if p.SearchPerDay != nil {
		perDay = *p.SearchPerDay
	}
	return perMinute, perDay
}

func (p PoolConfig) DeliverCap() int {
	if p.DeliverMaxBytes == 0 {
		return DefaultDeliverMaxBytes
	}
	return p.DeliverMaxBytes
}

func (p PoolConfig) WorkerNames() []string {
	out := make([]string, len(p.Workers))
	for i, s := range p.Workers {
		out[i] = string(s)
	}
	return out
}

func (l *Loaded) PoolDaemon() bool { return len(l.cfg().Worker.Pools) > 0 }

func (l *Loaded) Pools() []string { return sortedKeys(l.cfg().Worker.Pools) }

func (l *Loaded) Pool(name string) (PoolConfig, bool) {
	p, ok := l.cfg().Worker.Pools[name]
	return p, ok
}

var leaseSuffix = regexp.MustCompile(`-g[0-9]+$`)

func nameHasSeparator(name string) bool {
	return name == "" || strings.ContainsAny(name, ":/@* \t")
}

const bearerURLWant = "want https, or http to a loopback host (127.0.0.0/8, ::1, localhost), with a host and no credentials in the URL: a bearer goes with every call"

func SayOverUnix(raw string) (socket, path string, ok bool) {
	rest, found := strings.CutPrefix(raw, "unix:")
	if !found {
		return "", "", false
	}
	socket, path, _ = strings.Cut(rest, ":")
	if path == "" {
		path = "/"
	}
	u, err := url.ParseRequestURI(path)
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return "", "", false
	}
	return socket, path, true
}

func carriesABearerSafely(raw string) bool {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.User != nil:
		return false
	case u.Scheme == "https":
		return true
	case u.Scheme != "http":
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func refuseUnknownWorkerKeys(md toml.MetaData) error {
	for _, k := range md.Undecoded() {
		if len(k) > 0 && k[0] == "slot" {
			return fmt.Errorf("%s: not a key figaro knows; a pool reads no key it does not know, so a misspelt one cannot leave it open", k)
		}
	}
	return nil
}

func (c Config) validateWorker() error {
	owner := map[string]string{}
	for _, name := range sortedKeys(c.Worker.Pools) {
		p := c.Worker.Pools[name]
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
		case p.SayPerMinute != nil && *p.SayPerMinute < 0:
			return fmt.Errorf("%s.say_per_minute %d is negative", where, *p.SayPerMinute)
		case p.DeliverMaxBytes < 0:
			return fmt.Errorf("%s.deliver_max_bytes %d is negative", where, p.DeliverMaxBytes)
		case p.SearchCredential != "" && filepath.Base(p.SearchCredential) != p.SearchCredential:
			return fmt.Errorf("%s.search_credential %q: want the name of a file in the unit's credentials directory, not a path", where, p.SearchCredential)
		case p.SearchCredential == "." || p.SearchCredential == "..":
			return fmt.Errorf("%s.search_credential %q: want the name of a file", where, p.SearchCredential)
		case p.SearchCredential != "" && p.SearchURL == "":
			return fmt.Errorf("%s.search_credential with no search_url: a credential for a search the pool does not make", where)
		case p.SearchPerMinute != nil && *p.SearchPerMinute < 0:
			return fmt.Errorf("%s.search_per_minute %d is negative", where, *p.SearchPerMinute)
		case p.SearchPerDay != nil && *p.SearchPerDay < 0:
			return fmt.Errorf("%s.search_per_day %d is negative", where, *p.SearchPerDay)
		case len(p.Workers) == 0:
			return fmt.Errorf("%s.slots: a pool with no slot can lease nothing", where)
		}
		if u, err := url.Parse(p.Rou2URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return fmt.Errorf("%s.rou2_url %q: want rou2's root, an http or https URL with a host and nothing after it; leases are opened at <root>/v1/leases", where, p.Rou2URL)
		}
		if _, _, overUnix := SayOverUnix(p.SayURL); !overUnix && !carriesABearerSafely(p.SayURL) {
			return fmt.Errorf("%s.say_url %q: %s, or unix:<absolute socket path>[:<request path>]", where, p.SayURL, bearerURLWant)
		}
		if p.SearchURL != "" && !carriesABearerSafely(p.SearchURL) {
			return fmt.Errorf("%s.search_url %q: %s", where, p.SearchURL, bearerURLWant)
		}
		for _, worker := range p.WorkerNames() {
			switch {
			case nameHasSeparator(worker):
				return fmt.Errorf("%s.slots: %q: a slot name has no : / @ * or space", where, worker)
			case worker == "local" || worker == "bwrap" || worker == "self":
				return fmt.Errorf("%s.slots: %s is reserved", where, worker)
			case leaseSuffix.MatchString(worker):
				return fmt.Errorf("%s.slots: %q ends like a lease (<slot>-g<generation>); a slot name does not", where, worker)
			}
			if other, taken := owner[worker]; taken {
				return fmt.Errorf("%s.slots: %q is also a slot of %s; a lease is named after its slot alone", where, worker, other)
			}
			owner[worker] = where
		}
	}
	return nil
}

func (c Config) refuseWhatAPoolDaemonCannotHold(md toml.MetaData, providerFiles []string, serviceTokenOf func(provider string) string) error {
	if len(c.Worker.Pools) == 0 {
		return nil
	}
	for _, table := range [][]string{{"troupe"}, {"peers"}, {"door", "tls"}, {"sandbox", "boxes"}, {"authz", "api_keys"}} {
		if md.IsDefined(table...) {
			return fmt.Errorf("[%s] beside [slot.pools]: a daemon that leases slots holds no troupe, peer, node door, static box or api key", strings.Join(table, "."))
		}
	}
	named := map[string]bool{}
	for _, p := range c.Worker.Pools {
		if token := serviceTokenOf(p.Provider); token != "" {
			named[token] = true
		}
	}
	for _, name := range sortedKeys(c.ServiceTokens) {
		if !named[name] {
			return fmt.Errorf("[service_tokens.%s] beside [slot.pools]: no pool's provider names it, and a daemon that leases slots holds no outbound credential but its pools' providers'", name)
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
	for _, p := range c.Worker.Pools {
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
