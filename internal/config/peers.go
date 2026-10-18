package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
)

type TLSDoorConfig struct {
	Listen   string `toml:"listen"`
	Cert     string `toml:"cert"`
	Key      string `toml:"key"`
	ClientCA string `toml:"client_ca"`
}

type PeerConfig struct {
	Endpoint string `toml:"endpoint"`
	Identity string `toml:"identity"`
	CA       string `toml:"ca"`
	Cert     string `toml:"cert"`
	Key      string `toml:"key"`
}

type TroupeConfig struct {
	Version int64 `toml:"version"`
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
	Listen   string
	Cert     string
	Key      string
	ClientCA string
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
	out := TLSDoor{Listen: c.Listen, Cert: l.credPath(c.Cert), Key: l.credPath(c.Key), ClientCA: l.credPath(c.ClientCA)}
	for name, v := range map[string]string{"cert": out.Cert, "key": out.Key, "client_ca": out.ClientCA} {
		if v == "" {
			return TLSDoor{}, false, fmt.Errorf("door.tls: %s is required", name)
		}
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
	id, err := url.Parse(c.Identity)
	if err != nil || id.Scheme == "" || id.Opaque != "" {
		return Peer{}, fmt.Errorf("identity %q is not a URI", c.Identity)
	}
	out := Peer{Node: node, Address: addr, Identity: c.Identity, CA: l.credPath(c.CA), Cert: l.credPath(c.Cert), Key: l.credPath(c.Key)}
	for name, v := range map[string]string{"ca": out.CA, "cert": out.Cert, "key": out.Key} {
		if v == "" {
			return Peer{}, fmt.Errorf("%s is required", name)
		}
	}
	return out, nil
}

func (l *Loaded) credPath(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(l.dir(), p)
}
