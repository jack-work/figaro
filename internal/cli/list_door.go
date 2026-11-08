package cli

import (
	"context"
	"maps"
	"slices"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func listWithHome(ctx context.Context, acli *sdk.Angelus, req rpc.ListRequest) (*rpc.ListResponse, error) {
	if !resolveDoor().configured {
		return acli.ListWith(ctx, req)
	}
	home, err := sdk.DialAngelus(transport.UnixEndpoint(boot.AngelusSocketPath()))
	if err != nil {
		return acli.ListWith(ctx, req)
	}
	defer home.Close()
	homeHello, err := home.Hello(ctx)
	if err != nil || homeHello.Node == "" {
		return acli.ListWith(ctx, req)
	}
	doorHello, err := acli.Hello(ctx)
	if err != nil || doorHello.Node == homeHello.Node {
		return acli.ListWith(ctx, req)
	}

	away := req
	away.ExceptNodes = append(slices.Clone(req.ExceptNodes), homeHello.Node)
	resp, err := acli.ListWith(ctx, away)
	if err != nil {
		return nil, err
	}
	covered := nodesAnswering(resp, doorHello.Node)
	mine := req
	mine.ExceptNodes = append(slices.Clone(req.ExceptNodes), slices.Sorted(maps.Keys(covered))...)
	local, err := home.ListWith(ctx, mine)
	if err != nil {
		resp.Unreachable = append(resp.Unreachable, rpc.NodeUnreachable{Node: homeHello.Node, Error: err.Error()})
		return resp, nil
	}
	return mergeHome(resp, local, homeHello.Node, doorHello.Node, covered), nil
}

func nodesAnswering(resp *rpc.ListResponse, door string) map[string]bool {
	covered := map[string]bool{door: true}
	for _, r := range resp.Figaros {
		if r.Node != "" {
			covered[r.Node] = true
		}
	}
	for _, u := range resp.Unreachable {
		covered[u.Node] = true
	}
	return covered
}

func mergeHome(away, home *rpc.ListResponse, homeNode, door string, covered map[string]bool) *rpc.ListResponse {
	out := &rpc.ListResponse{NextBefore: away.NextBefore}
	for _, r := range away.Figaros {
		if r.Node != homeNode {
			out.Figaros = append(out.Figaros, r)
		}
	}
	for _, u := range away.Unreachable {
		if u.Node != homeNode {
			u.Error += " (as seen from node " + door[:min(rpc.NodeShortLen, len(door))] + "; this node did not try it)"
			out.Unreachable = append(out.Unreachable, u)
		}
	}
	for _, r := range home.Figaros {
		if r.Node == "" {
			r.Node = homeNode
		}
		if r.Node == homeNode || !covered[r.Node] {
			out.Figaros = append(out.Figaros, r)
		}
	}
	for _, u := range home.Unreachable {
		if !covered[u.Node] {
			out.Unreachable = append(out.Unreachable, u)
		}
	}
	return out
}
