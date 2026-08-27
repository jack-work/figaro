package angelus

// The angelus.peers handler, and the federation half of figaro.list.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

func (h *handlers) peersHandler(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.PeerRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}
	p := h.angelus.Peers
	if p == nil {
		return nil, fmt.Errorf("this daemon has no peer registry")
	}

	switch {
	case req.Peer != nil:
		if req.Peer.Name == "" || req.Peer.URL == "" {
			return nil, fmt.Errorf("a peer needs a name and a url")
		}
		peer := Peer{Name: req.Peer.Name, URL: req.Peer.URL, TokenFile: req.Peer.TokenFile}
		// FAIL AT ADD TIME, not at the first listing. An endpoint that
		// cannot be built -- a bad scheme, an unreadable token file -- is a
		// mistake the operator is standing right there to fix, and finding
		// it later means finding it as a mysteriously absent machine.
		if _, err := peer.endpoint(); err != nil {
			return nil, err
		}
		if err := p.put(peer); err != nil {
			return nil, err
		}
	case req.Remove != "":
		ok, err := p.remove(req.Remove)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("no peer named %q", req.Remove)
		}
	}

	list := p.list()
	resp := rpc.PeerResponse{Peers: make([]rpc.PeerSpec, 0, len(list))}
	for _, peer := range list {
		resp.Peers = append(resp.Peers, rpc.PeerSpec{
			Name: peer.Name, URL: peer.URL, TokenFile: peer.TokenFile,
		})
	}
	resp.Reachable = h.probePeers(ctx, list)
	return resp, nil
}

// probePeers asks each peer who it is. It is part of `peer ls` rather than a
// separate verb because "which of these can I actually reach" is the only
// question anyone asks of a peer list, and answering it later costs a second
// round trip and a second chance to be wrong.
func (h *handlers) probePeers(ctx context.Context, list []Peer) map[string]string {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]string, len(list))
	type res struct{ name, val string }
	ch := make(chan res, len(list))
	for _, peer := range list {
		go func(peer Peer) {
			r := res{name: peer.Name}
			defer func() { ch <- r }()
			ep, err := peer.endpoint()
			if err != nil {
				r.val = "error: " + err.Error()
				return
			}
			pctx, cancel := context.WithTimeout(ctx, peerFanoutTimeout)
			defer cancel()
			cli, err := sdk.DialAngelus(ep)
			if err != nil {
				r.val = "unreachable: " + err.Error()
				return
			}
			defer cli.Close()
			st, err := cli.Status(pctx)
			if err != nil {
				r.val = "error: " + err.Error()
				return
			}
			r.val = "ok build=" + st.Build
		}(peer)
	}
	for range list {
		r := <-ch
		out[r.name] = r.val
	}
	return out
}
