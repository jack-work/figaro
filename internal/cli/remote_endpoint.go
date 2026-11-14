package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/sdk"
)

const troupeProbeTimeout = 5 * time.Second

var troupeHTTPClient *http.Client

type endpointRecord struct {
	Node     string `json:"node"`
	ProbedMS int64  `json:"probed_ms"`
}

type endpointCacheFile struct {
	Endpoints map[string]endpointRecord `json:"endpoints"`
}

func troupeEndpointsPath() string { return filepath.Join(boot.CacheDir(), "troupe-endpoints.json") }

func readEndpointCache() map[string]endpointRecord {
	out := map[string]endpointRecord{}
	raw, err := os.ReadFile(troupeEndpointsPath())
	if err != nil {
		return out
	}
	var f endpointCacheFile
	if json.Unmarshal(raw, &f) != nil {
		return out
	}
	for url, r := range f.Endpoints {
		if rpc.ValidateAriaID(r.Node) == nil {
			out[url] = r
		}
	}
	return out
}

func writeEndpointCache(endpoints map[string]endpointRecord) error {
	path := troupeEndpointsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(endpointCacheFile{Endpoints: endpoints}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".troupe-endpoints.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type troupeEndpoint struct {
	url      string
	profiles []boot.Troupe
}

func troupeEndpoints(loaded *config.Loaded) []troupeEndpoint {
	names, _ := boot.TroupeNames(loaded)
	byURL := map[string]*troupeEndpoint{}
	var urls []string
	for _, n := range names {
		tr, err := boot.LoadTroupe(loaded, n)
		if err != nil || !tr.Remote() {
			continue
		}
		d, ok := byURL[tr.URL]
		if !ok {
			d = &troupeEndpoint{url: tr.URL}
			byURL[tr.URL] = d
			urls = append(urls, tr.URL)
		}
		d.profiles = append(d.profiles, tr)
	}
	sort.Strings(urls)
	out := make([]troupeEndpoint, len(urls))
	for i, u := range urls {
		out[i] = *byURL[u]
	}
	return out
}

func (d troupeEndpoint) ordered(own string) []boot.Troupe {
	ps := slices.Clone(d.profiles)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Name == own && ps[j].Name != own })
	return ps
}

func (d troupeEndpoint) endpoint(own string) (transport.Endpoint, string, error) {
	ps := d.ordered(own)
	for _, tr := range ps {
		bearer := troupeBearer(tr)
		if bearer == nil {
			continue
		}
		if _, err := bearer.Resolve(); err != nil {
			continue
		}
		ep := transport.WSSEndpoint(tr.URL, renewingBearer{Bearer: bearer, name: tr.Name})
		ep.HTTPClient = troupeHTTPClient
		return ep, tr.Name, nil
	}
	return transport.Endpoint{}, ps[0].Name, troupeLoginLine(ps[0].Name)
}

type renewingBearer struct {
	transport.Bearer
	name string
}

func (b renewingBearer) Renewal() string { return troupeLoginLine(b.name).Error() }

func troupeLoginLine(name string) error { return fmt.Errorf("figaro troupe login %s", name) }

func refusedCredential(err error) bool {
	s := err.Error()
	return strings.Contains(s, "HTTP 401") || strings.Contains(s, "(401)") || strings.Contains(s, ": credential: ")
}

var probeTroupeNode = func(ep transport.Endpoint) (string, error) {
	acli, err := sdk.DialAngelus(ep)
	if err != nil {
		return "", err
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), troupeProbeTimeout)
	defer cancel()
	h, err := acli.Hello(ctx)
	if err != nil {
		return "", err
	}
	if rpc.ValidateAriaID(h.Node) != nil {
		return "", fmt.Errorf("%s names no node", ep.Address)
	}
	return h.Node, nil
}

func nodeMatches(full, ref string) bool {
	return full == ref || len(ref) >= rpc.NodeShortLen && strings.HasPrefix(full, ref)
}

type nodeRouter struct {
	loaded    *config.Loaded
	own       string
	endpoints []troupeEndpoint
	cache     map[string]endpointRecord
	probed    map[string]bool
	logins    map[string]error
}

func newNodeRouter(loaded *config.Loaded) *nodeRouter {
	own := ""
	if loaded != nil {
		own = loaded.Config.Troupe.Name
	}
	return &nodeRouter{
		loaded:    loaded,
		own:       own,
		endpoints: troupeEndpoints(loaded),
		cache:     readEndpointCache(),
		probed:    map[string]bool{},
		logins:    map[string]error{},
	}
}

func (r *nodeRouter) claims(node string) []troupeEndpoint {
	var out []troupeEndpoint
	for _, d := range r.endpoints {
		if rec, ok := r.cache[d.url]; ok && nodeMatches(rec.Node, node) {
			out = append(out, d)
		}
	}
	return out
}

func (r *nodeRouter) probe(endpoints []troupeEndpoint) {
	type answer struct {
		url  string
		node string
		err  error
	}
	var pending []troupeEndpoint
	for _, d := range endpoints {
		if !r.probed[d.url] {
			r.probed[d.url] = true
			pending = append(pending, d)
		}
	}
	if len(pending) == 0 {
		return
	}
	answers := make(chan answer, len(pending))
	for _, d := range pending {
		ep, name, err := d.endpoint(r.own)
		if err != nil {
			answers <- answer{url: d.url, err: err}
			continue
		}
		go func() {
			node, err := probeTroupeNode(ep)
			if err != nil && refusedCredential(err) {
				err = troupeLoginLine(name)
			}
			answers <- answer{url: d.url, node: node, err: err}
		}()
	}
	changed := false
	for range pending {
		a := <-answers
		if a.err != nil {
			r.logins[a.url] = a.err
			continue
		}
		if rec, ok := r.cache[a.url]; !ok || rec.Node != a.node {
			changed = true
		}
		r.cache[a.url] = endpointRecord{Node: a.node, ProbedMS: time.Now().UnixMilli()}
	}
	if changed {
		if err := writeEndpointCache(r.cache); err != nil {
			fmt.Fprintf(stderrw, "figaro: troupe door cache: %s\n", err)
		}
	}
}

func (r *nodeRouter) endpoint(node string) (transport.Endpoint, error) {
	if len(r.endpoints) == 0 {
		return transport.Endpoint{}, fmt.Errorf("no troupe profile reaches node %s (troupes/*.toml has no wss door)", node)
	}
	found := r.claims(node)
	if len(found) != 1 {
		r.probe(r.endpoints)
		found = r.claims(node)
	}
	switch len(found) {
	case 1:
		ep, _, err := found[0].endpoint(r.own)
		return ep, err
	case 0:
		for _, d := range r.endpoints {
			if err, ok := r.logins[d.url]; ok && isLoginLine(err) {
				return transport.Endpoint{}, err
			}
		}
		return transport.Endpoint{}, fmt.Errorf("no troupe profile reaches node %s", node)
	}
	urls := make([]string, len(found))
	for i, d := range found {
		urls[i] = d.url
	}
	return transport.Endpoint{}, fmt.Errorf("node %s answers at %d doors (%s): refused", node, len(found), strings.Join(urls, ", "))
}

func isLoginLine(err error) bool { return strings.HasPrefix(err.Error(), "figaro troupe login ") }

type nodeEndpointKey struct{ node, configDir, cacheDir string }

var (
	nodeEndpointMu     sync.Mutex
	nodeEndpointCached map[nodeEndpointKey]transport.Endpoint
	ownNodeCached      map[string]string
)

func nodeEndpoint(node string) (transport.Endpoint, error) {
	k := nodeEndpointKey{node, config.DefaultConfigDir(), boot.CacheDir()}
	nodeEndpointMu.Lock()
	defer nodeEndpointMu.Unlock()
	if ep, ok := nodeEndpointCached[k]; ok {
		return ep, nil
	}
	loaded, _ := config.Load(k.configDir)
	for range 2 {
		r := newNodeRouter(loaded)
		ep, err := r.endpoint(node)
		if err != nil {
			return transport.Endpoint{}, err
		}
		answered, err := checkRoutedEndpoint(ep)
		if err != nil {
			return transport.Endpoint{}, err
		}
		if nodeMatches(answered, node) {
			if nodeEndpointCached == nil {
				nodeEndpointCached = map[nodeEndpointKey]transport.Endpoint{}
			}
			nodeEndpointCached[k] = ep
			return ep, nil
		}
		r.learned(ep.Address, answered)
	}
	return transport.Endpoint{}, fmt.Errorf("no troupe profile reaches node %s", node)
}

func (r *nodeRouter) learned(url, node string) {
	r.cache[url] = endpointRecord{Node: node, ProbedMS: time.Now().UnixMilli()}
	if err := writeEndpointCache(r.cache); err != nil {
		fmt.Fprintf(stderrw, "figaro: troupe door cache: %s\n", err)
	}
}

var checkRoutedEndpoint = func(ep transport.Endpoint) (string, error) {
	acli, err := sdk.DialAngelus(ep)
	if err != nil {
		return "", err
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), troupeProbeTimeout)
	defer cancel()
	h, err := acli.Hello(ctx)
	if err != nil {
		return "", err
	}
	st, err := acli.Status(ctx)
	if err != nil {
		return "", err
	}
	d, mine := identityOf(st), boot.BuildRevision()
	switch buildHandshake(d, mine, boot.BuildRelease()) {
	case handshakeProtocolMismatch:
		return "", fmt.Errorf("the daemon at %s speaks protocol %d and this CLI speaks %d", ep.Address, d.protocol, rpc.ProtocolVersion)
	case handshakeRefuse:
		return "", fmt.Errorf("the daemon at %s is build %s and this CLI is %s, and it reports no protocol to check", ep.Address, short12(d.build), short12(mine))
	}
	return h.Node, nil
}

func ownNode() string {
	root := boot.AriaRoot()
	nodeEndpointMu.Lock()
	defer nodeEndpointMu.Unlock()
	if id, ok := ownNodeCached[root]; ok {
		return id
	}
	id, _ := store.ReadNodeIDAt(root)
	if ownNodeCached == nil {
		ownNodeCached = map[string]string{}
	}
	ownNodeCached[root] = id
	return id
}

func foreignNode(id string) string {
	node, _ := rpc.SplitNode(id)
	if node == "" {
		return ""
	}
	if own := ownNode(); own != "" && nodeMatches(own, node) {
		return ""
	}
	return node
}

func endpointFor(id string) (sdk.Addr, error) {
	d := resolveEndpoint()
	if d.configured {
		return sdk.Addr{Endpoint: d.ep, ID: id}, nil
	}
	node := foreignNode(id)
	if node == "" {
		return sdk.Addr{Endpoint: d.ep, ID: id}, nil
	}
	ep, err := nodeEndpoint(node)
	if err != nil {
		return sdk.Addr{}, err
	}
	return sdk.Addr{Endpoint: ep, ID: id}, nil
}
