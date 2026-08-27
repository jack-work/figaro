package angelus

// PEERS: other angeli this daemon federates with.
//
// The shape follows one rule from the design: THE CLI STAYS DUMB. It has one
// transport, the local unix socket, holds no credentials and knows no URLs.
// Everything remote is a richer ANSWER from the local daemon, never a new
// thing the CLI has to do.
//
// So the daemon holds the peer list, holds the credentials, dials outward,
// and merges. `figaro ls` is one local call whose answer happens to span
// machines.
//
// This is the control plane. The data plane -- an aria's stream, its form
// deltas, its turns -- is meant to connect DIRECTLY to the node that owns
// the aria rather than being relayed through here, because relaying every
// frame would double each hop and make this process the choke point. That
// is the next piece; this one establishes who exists and where.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

// Peer is one federated node.
type Peer struct {
	Name string `json:"name"`
	// URL is the peer's gateway, e.g. https://fig.kelliher.info.
	URL string `json:"url"`
	// TokenFile is where the bearer for this peer lives. THE PATH IS STORED,
	// NOT THE SECRET: this file is written to the state directory in plain
	// JSON, and a token in it would be a token on disk in the clear beside
	// every aria. The file it names is the operator's to protect.
	TokenFile string `json:"token_file,omitempty"`
}

// token reads the peer's bearer. Absent is not an error -- a peer behind a
// proxy that authenticates some other way needs none.
func (p Peer) token() (string, error) {
	if p.TokenFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return "", fmt.Errorf("peer %s: read token: %w", p.Name, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func (p Peer) endpoint() (transport.Endpoint, error) {
	tok, err := p.token()
	if err != nil {
		return transport.Endpoint{}, err
	}
	u := strings.TrimRight(p.URL, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		return transport.Endpoint{Scheme: "https", Address: strings.TrimPrefix(u, "https://"), Bearer: tok}, nil
	case strings.HasPrefix(u, "http://"):
		return transport.Endpoint{Scheme: "http", Address: strings.TrimPrefix(u, "http://"), Bearer: tok}, nil
	default:
		return transport.Endpoint{}, fmt.Errorf("peer %s: want an http(s):// URL, got %q", p.Name, p.URL)
	}
}

// peers is the durable set, kept as a plain file beside the store. It is
// deliberately NOT a form: forms fork with arias, and the set of machines
// this daemon can reach is a property of the daemon rather than of any
// conversation in it.
type peers struct {
	mu   sync.Mutex
	path string
	m    map[string]Peer
}

func newPeers(stateDir string) *peers {
	p := &peers{path: filepath.Join(stateDir, "peers.json"), m: map[string]Peer{}}
	p.load()
	return p
}

func (p *peers) load() {
	b, err := os.ReadFile(p.path)
	if err != nil {
		return // absent is the normal first state
	}
	var list []Peer
	if err := json.Unmarshal(b, &list); err != nil {
		slog.Warn("peers file is unreadable; starting with none", "path", p.path, "err", err)
		return
	}
	for _, peer := range list {
		p.m[peer.Name] = peer
	}
}

func (p *peers) save() error {
	list := p.list()
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	// Write-then-rename: a crash mid-write must not leave a truncated file
	// that the next start reads as "no peers".
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

func (p *peers) list() []Peer {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Peer, 0, len(p.m))
	for _, peer := range p.m {
		out = append(out, peer)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p *peers) put(peer Peer) error {
	p.mu.Lock()
	p.m[peer.Name] = peer
	p.mu.Unlock()
	return p.save()
}

func (p *peers) remove(name string) (bool, error) {
	p.mu.Lock()
	_, ok := p.m[name]
	delete(p.m, name)
	p.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, p.save()
}

// peerFanoutTimeout bounds ONE peer's contribution to a listing. A peer that
// is down must cost a slow `figaro ls`, never a hung one: the local answer
// is always available and a federated view that blocks on the worst node is
// worse than one that says a node is unreachable.
const peerFanoutTimeout = 6 * time.Second

// federate asks every peer for its listing and returns the rows, tagged with
// the node they came from. Errors are RETURNED, not swallowed: a listing
// that silently omits an unreachable machine tells the reader the arias are
// gone, which is a different and much worse claim than "I could not ask".
func (p *peers) federate(ctx context.Context, req rpc.ListRequest) ([]rpc.FigaroInfoResponse, map[string]string) {
	list := p.list()
	if len(list) == 0 {
		return nil, nil
	}

	type result struct {
		rows []rpc.FigaroInfoResponse
		err  error
		name string
	}
	ch := make(chan result, len(list))

	for _, peer := range list {
		go func(peer Peer) {
			r := result{name: peer.Name}
			defer func() { ch <- r }()

			ep, err := peer.endpoint()
			if err != nil {
				r.err = err
				return
			}
			pctx, cancel := context.WithTimeout(ctx, peerFanoutTimeout)
			defer cancel()

			cli, err := sdk.DialAngelus(ep)
			if err != nil {
				r.err = err
				return
			}
			defer cli.Close()

			resp, err := cli.ListWith(pctx, req)
			if err != nil {
				r.err = err
				return
			}
			for i := range resp.Figaros {
				// The row says where it lives. Without this a federated
				// listing is a pile of ids with no way to act on them.
				resp.Figaros[i].Node = peer.Name
			}
			r.rows = resp.Figaros
		}(peer)
	}

	var rows []rpc.FigaroInfoResponse
	var errs map[string]string
	for range list {
		r := <-ch
		if r.err != nil {
			if errs == nil {
				errs = map[string]string{}
			}
			errs[r.name] = r.err.Error()
			continue
		}
		rows = append(rows, r.rows...)
	}
	return rows, errs
}
