package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/troupe"
)

type TLSDoorConfig struct {
	Listen    string `toml:"listen"`
	Advertise string `toml:"advertise"`
	Cert      string `toml:"cert"`
	Key       string `toml:"key"`
	ClientCA  string `toml:"client_ca"`
}

type PeerConfig struct {
	Endpoint string `toml:"endpoint"`
	Identity string `toml:"identity"`
	CA       string `toml:"ca"`
	Cert     string `toml:"cert"`
	Key      string `toml:"key"`
}

type TroupeConfig struct {
	Name     string `toml:"name"`
	Version  int64  `toml:"version"`
	Provider string `toml:"provider"`
	Policy   string `toml:"policy"`
}

const (
	ProviderStatic = "static"
	ProviderFigaro = "figaro"
)

func (l *Loaded) TroupeProvider() (string, error) {
	p := l.cfg().Troupe.Provider
	switch p {
	case "", ProviderStatic:
		return ProviderStatic, nil
	case ProviderFigaro:
	default:
		return "", fmt.Errorf("troupe.provider %q: want %s or %s", p, ProviderStatic, ProviderFigaro)
	}
	name, err := l.TroupeName()
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", errors.New("troupe.provider figaro needs troupe.name")
	}
	if len(l.cfg().Peers) > 0 {
		return "", errors.New("troupe.provider figaro takes its members from the troupe, so [peers] must be empty")
	}
	if l.cfg().Troupe.Version != 0 {
		return "", errors.New("troupe.version belongs to the static provider")
	}
	return ProviderFigaro, nil
}

func (l *Loaded) TroupeName() (string, error) {
	name := l.cfg().Troupe.Name
	if name == "" {
		return "", nil
	}
	if err := troupe.ValidName(name); err != nil {
		return "", fmt.Errorf("troupe.name: %w", err)
	}
	return name, nil
}

func (l *Loaded) troupeKeyed(files map[string]string) (bool, error) {
	set := 0
	for _, v := range files {
		if v != "" {
			set++
		}
	}
	if set == len(files) {
		return false, nil
	}
	name, err := l.TroupeName()
	if err != nil {
		return false, err
	}
	if set == 0 && name != "" {
		return true, nil
	}
	for k, v := range files {
		if v == "" {
			if name == "" {
				return false, fmt.Errorf("%s is required (or set [troupe] name to use the troupe's keys)", k)
			}
			return false, fmt.Errorf("%s is missing: give every file, or none to use troupe %s's keys", k, name)
		}
	}
	return false, nil
}

func (l *Loaded) StaticTroupePolicy() (string, error) {
	t := l.cfg().Troupe
	if t.Policy == "" {
		return "", nil
	}
	if t.Provider == ProviderFigaro {
		return "", errors.New("troupe.policy: a figaro troupe's policy is signed and gossiped; set it with figaro troupe policy set")
	}
	if t.Name == "" {
		return "", errors.New("troupe.policy needs troupe.name")
	}
	if filepath.IsAbs(t.Policy) {
		return t.Policy, nil
	}
	return filepath.Join(l.dir(), t.Policy), nil
}

func (l *Loaded) TroupeVersion() (uint64, error) {
	v := l.cfg().Troupe.Version
	if v < 0 {
		return 0, fmt.Errorf("troupe.version %d: want 0 or more", v)
	}
	return uint64(v), nil
}

type FederationConfig struct {
	DirectorySize *int `toml:"directory_size"`
}

const defaultDirectorySize = 32768

func (l *Loaded) DirectorySize() int {
	n := l.cfg().Federation.DirectorySize
	if n == nil || *n <= 0 {
		return defaultDirectorySize
	}
	return *n
}

type TLSDoor struct {
	Listen    string
	Advertise string
	Cert      string
	Key       string
	ClientCA  string
}

type Peer struct {
	Node     string
	Address  string
	Identity string
	CA       string
	Cert     string
	Key      string
}

const peerScheme = "tls://"

func (l *Loaded) TLSDoor() (TLSDoor, bool, error) {
	c := l.cfg().Door.TLS
	if c == nil {
		return TLSDoor{}, false, nil
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return TLSDoor{}, false, fmt.Errorf("door.tls: listen %q is not host:port", c.Listen)
	}
	out := TLSDoor{Listen: c.Listen, Advertise: c.Advertise, Cert: l.credPath(c.Cert), Key: l.credPath(c.Key), ClientCA: l.credPath(c.ClientCA)}
	if out.Advertise == "" {
		out.Advertise = out.Listen
	}
	if host, _, err := net.SplitHostPort(out.Advertise); err != nil || host == "" || net.ParseIP(host).IsUnspecified() {
		out.Advertise = ""
	}
	if c.Advertise != "" && out.Advertise == "" {
		return TLSDoor{}, false, fmt.Errorf("door.tls: advertise %q is not a host:port another node can dial", c.Advertise)
	}
	if _, err := l.troupeKeyed(map[string]string{"cert": out.Cert, "key": out.Key, "client_ca": out.ClientCA}); err != nil {
		return TLSDoor{}, false, fmt.Errorf("door.tls: %w", err)
	}
	return out, true, nil
}

func (l *Loaded) Peers() ([]Peer, error) {
	nodes := make([]string, 0, len(l.cfg().Peers))
	for node := range l.cfg().Peers {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	out := make([]Peer, 0, len(nodes))
	seen := map[string]string{}
	for _, node := range nodes {
		p, err := l.peer(node, l.cfg().Peers[node])
		if err != nil {
			return nil, fmt.Errorf("peers.%s: %w", node, err)
		}
		if other, dup := seen[p.Identity]; dup {
			return nil, fmt.Errorf("peers.%s: identity %s is already peers.%s", node, p.Identity, other)
		}
		seen[p.Identity] = node
		out = append(out, p)
	}
	return out, nil
}

func (l *Loaded) peer(node string, c PeerConfig) (Peer, error) {
	if err := rpc.ValidateAriaID(node); err != nil {
		return Peer{}, fmt.Errorf("node id: %w", err)
	}
	addr, ok := strings.CutPrefix(c.Endpoint, peerScheme)
	if !ok {
		return Peer{}, fmt.Errorf("endpoint %q: a peer is a %shost:port door", c.Endpoint, peerScheme)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return Peer{}, fmt.Errorf("endpoint %q: %w", c.Endpoint, err)
	}
	out := Peer{Node: node, Address: addr, Identity: c.Identity, CA: l.credPath(c.CA), Cert: l.credPath(c.Cert), Key: l.credPath(c.Key)}
	keyed, err := l.troupeKeyed(map[string]string{"ca": out.CA, "cert": out.Cert, "key": out.Key})
	if err != nil {
		return Peer{}, err
	}
	if out.Identity == "" && keyed {
		name, _ := l.TroupeName()
		out.Identity = troupe.Identity(name, node)
	}
	id, err := url.Parse(out.Identity)
	if err != nil || id.Scheme == "" || id.Opaque != "" {
		return Peer{}, fmt.Errorf("identity %q is not a URI", out.Identity)
	}
	return out, nil
}

func (l *Loaded) credPath(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(l.dir(), p)
}
