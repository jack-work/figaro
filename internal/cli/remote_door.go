package cli

import (
	"context"
	"encoding/json"
	"fmt"
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

type doorRecord struct {
	Node     string `json:"node"`
	ProbedMS int64  `json:"probed_ms"`
}

type doorCacheFile struct {
	Doors map[string]doorRecord `json:"doors"`
}

func troupeDoorsPath() string { return filepath.Join(boot.CacheDir(), "troupe-doors.json") }

func readDoorCache() map[string]doorRecord {
	out := map[string]doorRecord{}
	raw, err := os.ReadFile(troupeDoorsPath())
	if err != nil {
		return out
	}
	var f doorCacheFile
	if json.Unmarshal(raw, &f) != nil {
		return out
	}
	for url, r := range f.Doors {
		if rpc.ValidateAriaID(r.Node) == nil {
			out[url] = r
		}
	}
	return out
}

func writeDoorCache(doors map[string]doorRecord) error {
	path := troupeDoorsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(doorCacheFile{Doors: doors}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".troupe-doors.*")
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

type troupeDoor struct {
	url      string
	profiles []boot.Troupe
}

func troupeDoors(loaded *config.Loaded) []troupeDoor {
	names, _ := boot.TroupeNames(loaded)
	byURL := map[string]*troupeDoor{}
	var urls []string
	for _, n := range names {
		tr, err := boot.LoadTroupe(loaded, n)
		if err != nil || !tr.Remote() {
			continue
		}
		d, ok := byURL[tr.URL]
		if !ok {
			d = &troupeDoor{url: tr.URL}
			byURL[tr.URL] = d
			urls = append(urls, tr.URL)
		}
		d.profiles = append(d.profiles, tr)
	}
	sort.Strings(urls)
	out := make([]troupeDoor, len(urls))
	for i, u := range urls {
		out[i] = *byURL[u]
	}
	return out
}

func (d troupeDoor) ordered(own string) []boot.Troupe {
	ps := slices.Clone(d.profiles)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Name == own && ps[j].Name != own })
	return ps
}

func (d troupeDoor) endpoint(own string) (transport.Endpoint, string, error) {
	ps := d.ordered(own)
	for _, tr := range ps {
		bearer := troupeBearer(tr)
		if bearer == nil {
			continue
		}
		if _, err := bearer.Resolve(); err != nil {
			continue
		}
		return transport.WSSEndpoint(tr.URL, bearer), tr.Name, nil
	}
	return transport.Endpoint{}, ps[0].Name, troupeLoginLine(ps[0].Name)
}

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
	loaded *config.Loaded
	own    string
	doors  []troupeDoor
	cache  map[string]doorRecord
	probed map[string]bool
	logins map[string]error
}

func newNodeRouter(loaded *config.Loaded) *nodeRouter {
	own := ""
	if loaded != nil {
		own = loaded.Config.Troupe.Name
	}
	return &nodeRouter{
		loaded: loaded,
		own:    own,
		doors:  troupeDoors(loaded),
		cache:  readDoorCache(),
		probed: map[string]bool{},
		logins: map[string]error{},
	}
}

func (r *nodeRouter) claims(node string) []troupeDoor {
	var out []troupeDoor
	for _, d := range r.doors {
		if rec, ok := r.cache[d.url]; ok && nodeMatches(rec.Node, node) {
			out = append(out, d)
		}
	}
	return out
}

func (r *nodeRouter) probe(doors []troupeDoor) {
	type answer struct {
		url  string
		node string
		err  error
	}
	var pending []troupeDoor
	for _, d := range doors {
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
		r.cache[a.url] = doorRecord{Node: a.node, ProbedMS: time.Now().UnixMilli()}
	}
	if changed {
		if err := writeDoorCache(r.cache); err != nil {
			fmt.Fprintf(stderrw, "figaro: troupe door cache: %s\n", err)
		}
	}
}

func (r *nodeRouter) door(node string) (transport.Endpoint, error) {
	if len(r.doors) == 0 {
		return transport.Endpoint{}, fmt.Errorf("no troupe profile reaches node %s (troupes/*.toml has no wss door)", node)
	}
	found := r.claims(node)
	if len(found) != 1 {
		r.probe(r.doors)
		found = r.claims(node)
	}
	switch len(found) {
	case 1:
		ep, _, err := found[0].endpoint(r.own)
		return ep, err
	case 0:
		for _, d := range r.doors {
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

type nodeDoorKey struct{ node, configDir, cacheDir string }

var (
	nodeDoorMu     sync.Mutex
	nodeDoorCached map[nodeDoorKey]transport.Endpoint
	ownNodeCached  map[string]string
)

func nodeDoor(node string) (transport.Endpoint, error) {
	k := nodeDoorKey{node, config.DefaultConfigDir(), boot.CacheDir()}
	nodeDoorMu.Lock()
	defer nodeDoorMu.Unlock()
	if ep, ok := nodeDoorCached[k]; ok {
		return ep, nil
	}
	loaded, _ := config.Load(k.configDir)
	ep, err := newNodeRouter(loaded).door(node)
	if err != nil {
		return transport.Endpoint{}, err
	}
	if nodeDoorCached == nil {
		nodeDoorCached = map[nodeDoorKey]transport.Endpoint{}
	}
	nodeDoorCached[k] = ep
	return ep, nil
}

func ownNode() string {
	root := boot.AriaRoot()
	nodeDoorMu.Lock()
	defer nodeDoorMu.Unlock()
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

func doorFor(id string) (sdk.Addr, error) {
	d := resolveDoor()
	if d.configured {
		return sdk.Addr{Door: d.ep, ID: id}, nil
	}
	node := foreignNode(id)
	if node == "" {
		return sdk.Addr{Door: d.ep, ID: id}, nil
	}
	ep, err := nodeDoor(node)
	if err != nil {
		return sdk.Addr{}, err
	}
	return sdk.Addr{Door: ep, ID: id}, nil
}
