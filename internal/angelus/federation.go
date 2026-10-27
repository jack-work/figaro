package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

const maxResolveRefs = 64

var errNoBearer = errors.New("nodes never vouch for a caller: reaching another node needs a bearer (figaro troupe login <name>)")

type federation struct {
	members peer.Membership
	dialer  *peer.Dialer
	dir     peer.Directory
}

func (h *handlers) federate() {
	m := h.angelus.Peers
	if m == nil {
		return
	}
	ledger, growing := m.(*peer.Ledger)
	if !growing && len(m.Nodes()) == 0 {
		return
	}
	h.fed = &federation{members: m, dialer: &peer.Dialer{Members: m, Versions: h.angelus.PeerVersions}}
	h.fed.dir = peer.NewCache(m.Nodes, h.askPeer, h.config.DirectorySize())
	if growing {
		ledger.OnEvict(h.fed.dialer.Evict)
	}
}

func routable(method string) bool {
	switch method {
	case rpc.MethodHello, rpc.MethodBind, rpc.MethodResolve, rpc.MethodUnbind, rpc.MethodNodeResolve, rpc.MethodTroupeEntries, rpc.MethodTroupeSign, rpc.MethodTroupeIdentity, rpc.MethodTroupeLeave, rpc.MethodTroupePolicy, rpc.MethodTroupePolicySet,
		rpc.MethodTroupeAPIKeyMint, rpc.MethodTroupeAPIKeyRevoke, rpc.MethodTroupeAPIKeyList:
		return false
	}
	return true
}

func forwarded(ctx context.Context) bool {
	_, ok := peer.Via(ctx)
	return ok
}

func (h *handlers) routes(ctx context.Context, method string) bool {
	return h.fed != nil && routable(method) && !forwarded(ctx)
}

func (h *handlers) isSelf(node string) bool {
	own := h.nodeID()
	return own != "" && (node == own || len(node) >= rpc.NodeShortLen && strings.HasPrefix(own, node))
}

func (h *handlers) peerNamed(node string) (string, bool) {
	if h.fed == nil {
		return "", false
	}
	if _, ok := h.fed.members.Member(node); ok {
		return node, true
	}
	if len(node) < rpc.NodeShortLen {
		return "", false
	}
	var found []string
	for _, n := range h.fed.members.Nodes() {
		if strings.HasPrefix(n, node) {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

func (h *handlers) elsewhere(ctx context.Context, ref, node, id string, route bool) (string, string, error) {
	own := h.nodeID()
	if forwarded(ctx) {
		return "", "", fmt.Errorf("%s is on node %s, and this is node %s: a forwarded request is served here or refused", ref, node, own)
	}
	full, ok := h.peerNamed(node)
	if !ok {
		return "", "", fmt.Errorf("%s is on node %s, and this is node %s, which does not peer with it", ref, node, own)
	}
	if !route {
		return "", "", fmt.Errorf("%s is on node %s; this call is served only where it is made", ref, full)
	}
	return id, full, nil
}

func (h *handlers) locate(ctx context.Context, id string) (string, string, error) {
	if _, ok := authz.BearerOf(ctx); !ok {
		if node, known := h.fed.dir.Owner(id); known {
			return "", "", h.needsBearer(id, node)
		}
		return id, "", nil
	}
	node, full, err := h.fed.dir.Locate(ctx, id)
	if err != nil {
		return "", "", err
	}
	return full, node, nil
}

func (h *handlers) needsBearer(id, node string) error {
	return fmt.Errorf("%s is on node %s, and nodes never vouch for a caller: reaching it needs a bearer (figaro troupe login <name>)", id, node)
}

func (h *handlers) linkFor(ctx context.Context, node string) (*peer.Link, func(), error) {
	token, ok := authz.BearerOf(ctx)
	if !ok {
		return nil, nil, errNoBearer
	}
	if err := authz.ForwardableTo(ctx, node); err != nil {
		return nil, nil, err
	}
	p := rpc.Passage{Forwarded: true, Bearer: token}
	if f := followerOf(ctx); f != nil {
		l, err := f.link(ctx, h.fed.dialer, node, p)
		return l, func() {}, err
	}
	l, err := h.fed.dialer.Open(ctx, node, p, nil)
	if err != nil {
		return nil, nil, err
	}
	return l, func() { l.Close() }, nil
}

func (h *handlers) askPeer(ctx context.Context, node string, refs []string) (rpc.NodeResolveResponse, error) {
	l, done, err := h.linkFor(ctx, node)
	if err != nil {
		return rpc.NodeResolveResponse{}, err
	}
	defer done()
	resp, err := l.Resolve(ctx, refs)
	if refusedByPolicy(err) {
		return rpc.NodeResolveResponse{}, nil
	}
	return resp, err
}

func refusedByPolicy(err error) bool {
	var answered *jkrpc.Error
	return errors.As(err, &answered) && answered.Code == authz.ErrCode
}

func (h *handlers) forward(ctx context.Context, node, method string, params json.RawMessage) (any, error) {
	l, done, err := h.linkFor(ctx, node)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := l.Call(ctx, method, params)
	if to, ok := movedAway(err); ok && to != node && !h.isSelf(to) {
		h.fed.dir.Learn(rpc.AriaOf(params), to)
		l2, done2, lerr := h.linkFor(ctx, to)
		if lerr != nil {
			return nil, lerr
		}
		defer done2()
		node, l = to, l2
		raw, err = l.Call(ctx, method, params)
	}
	if err != nil {
		return nil, err
	}
	h.learnFrom(node, method, params, raw)
	return raw, nil
}

func movedAway(err error) (string, bool) {
	var answered *jkrpc.Error
	if !errors.As(err, &answered) || answered.Code != rpc.ErrCodeMoved {
		return "", false
	}
	var m rpc.Moved
	if json.Unmarshal(answered.Data, &m) != nil || m.Node == "" {
		return "", false
	}
	return m.Node, true
}

func (h *handlers) learnFrom(node, method string, params, raw json.RawMessage) {
	switch method {
	case rpc.MethodKill:
		var r rpc.KillResponse
		if json.Unmarshal(raw, &r) != nil || r.PurgeAt == 0 {
			h.fed.dir.Forget(rpc.AriaOf(params))
		}
	case rpc.MethodFork:
		var r rpc.ForkResponse
		if json.Unmarshal(raw, &r) == nil {
			for _, id := range []string{r.Continuation, r.Alternative} {
				if id != "" {
					h.fed.dir.Learn(id, node)
				}
			}
		}
	default:
		if id := rpc.AriaOf(raw); id != "" {
			h.fed.dir.Learn(id, node)
		}
	}
}

func (h *handlers) nodeResolve(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.NodeResolveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if len(req.Refs) > maxResolveRefs {
		return nil, fmt.Errorf("%s: at most %d refs at once", rpc.MethodNodeResolve, maxResolveRefs)
	}
	out := rpc.NodeResolveResponse{IDs: map[string]string{}}
	for _, ref := range req.Refs {
		if ref == "" || isCatalogID(ref) || strings.Contains(ref, rpc.NodeSeparator) {
			continue
		}
		if h.holdsMove(ref) != nil {
			continue
		}
		if h.knownID(ref) {
			if h.allowed(ctx, rpc.VerbRead, authz.Aria(ref)) == nil {
				out.IDs[ref] = ref
			}
			continue
		}
		if t, ok := h.tombstone(ref); ok {
			if t.State == store.MoveFinal && h.allowed(ctx, rpc.VerbRead, authz.Aria(ref)) == nil {
				if out.Moved == nil {
					out.Moved = map[string]rpc.Moved{}
				}
				out.Moved[ref] = rpc.Moved{ID: ref, Node: t.To}
			}
			continue
		}
		if len(strings.TrimPrefix(ref, "@")) < minIDPrefix || h.angelus.Backend == nil {
			continue
		}
		if m := h.matchReadable(ctx, ref); len(m) == 1 {
			out.IDs[ref] = m[0]
		}
	}
	return out, nil
}

type nodeListing struct {
	node string
	resp rpc.ListResponse
	err  error
}

func (h *handlers) listingPeers(next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		res, err := next(ctx, params)
		if err != nil || h.fed == nil || forwarded(ctx) {
			return res, err
		}
		local, ok := res.(rpc.ListResponse)
		if !ok {
			return res, nil
		}
		var req rpc.ListRequest
		_ = json.Unmarshal(params, &req)
		nodes := h.fed.members.Nodes()
		answers := make([]nodeListing, len(nodes))
		var wg sync.WaitGroup
		for i, node := range nodes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				answers[i] = h.listPeer(ctx, node, params)
			}()
		}
		wg.Wait()
		return h.mergeListings(req, local, answers)
	}
}

func (h *handlers) listPeer(ctx context.Context, node string, params json.RawMessage) nodeListing {
	out := nodeListing{node: node}
	l, done, err := h.linkFor(ctx, node)
	if err != nil {
		out.err = err
		return out
	}
	defer done()
	raw, err := l.Call(ctx, rpc.MethodList, params)
	if refusedByPolicy(err) {
		return out
	}
	if err == nil {
		err = json.Unmarshal(raw, &out.resp)
	}
	out.err = err
	return out
}

func (h *handlers) mergeListings(req rpc.ListRequest, local rpc.ListResponse, answers []nodeListing) (rpc.ListResponse, error) {
	merged := local
	more := local.NextBefore != ""
	for _, a := range answers {
		if a.err != nil {
			merged.Unreachable = append(merged.Unreachable, rpc.NodeUnreachable{Node: a.node, Error: a.err.Error()})
			continue
		}
		for _, row := range a.resp.Figaros {
			if row.Node == "" {
				row.Node = a.node
			}
			if row.State != "anchor" {
				h.fed.dir.Learn(row.ID, row.Node)
			}
			merged.Figaros = append(merged.Figaros, row)
		}
		merged.Unreachable = append(merged.Unreachable, a.resp.Unreachable...)
		more = more || a.resp.NextBefore != ""
	}
	merged.NextBefore = ""
	if !listBounded(req) {
		return merged, nil
	}
	slices.SortFunc(merged.Figaros, listOrder)
	if req.Limit > 0 && len(merged.Figaros) > req.Limit {
		merged.Figaros = merged.Figaros[:req.Limit:req.Limit]
		more = true
	}
	if more && len(merged.Figaros) > 0 {
		merged.NextBefore = listCursor(merged.Figaros[len(merged.Figaros)-1])
	}
	return merged, nil
}
