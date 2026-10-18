package angelus

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/store/xwal"
	"github.com/jack-work/jkrpc"
)

type mover interface {
	Component(id string) (xwal.Component, error)
	ExportComponent(id, token string) (*store.Outbound, error)
	BeginImport(token string, c xwal.Component) (*xwal.Graft, error)
	OpenImport(token string) (*xwal.Graft, error)
	CommitImport(g *xwal.Graft, digest string, metas map[string]json.RawMessage) error
	AbortImport(g *xwal.Graft) error
	RemoveMoved(c xwal.Component, token string) error
	Moves() *store.Moves
}

type moving struct {
	n  atomic.Int64
	mu sync.Mutex
	to map[string]string
}

func (m *moving) claim(ids []string, node string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if to, ok := m.to[id]; ok {
			return fmt.Errorf("%s is already moving to node %s", id, to)
		}
	}
	if m.to == nil {
		m.to = map[string]string{}
	}
	for _, id := range ids {
		m.to[id] = node
	}
	m.n.Store(int64(len(m.to)))
	return nil
}

func (m *moving) release(ids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.to, id)
	}
	m.n.Store(int64(len(m.to)))
}

func (m *moving) of(id string) (string, bool) {
	if m.n.Load() == 0 {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	to, ok := m.to[id]
	return to, ok
}

func errMoving(id, node string) error {
	return fmt.Errorf("%s is moving to node %s; try again when the move settles", id, node)
}

func (h *handlers) mover() (mover, error) {
	h.moverOnce.Do(func() {
		if m, ok := h.angelus.Backend.(mover); ok && m.Moves() != nil {
			h.mv, h.moves = m, m.Moves()
		}
	})
	if h.mv == nil {
		return nil, errors.New("this node's store cannot move arias")
	}
	return h.mv, nil
}

func (h *handlers) holdsMove(id string) error {
	if h.moving.n.Load() == 0 {
		return nil
	}
	if to, ok := h.moving.of(id); ok {
		return errMoving(id, to)
	}
	return nil
}

func moveToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (h *handlers) move(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.MoveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.fed == nil {
		return nil, errors.New("move: this node has no peers")
	}
	to, ok := h.peerNamed(req.To)
	if !ok {
		return nil, fmt.Errorf("move: node %s is not a peer of this node", req.To)
	}
	mv, err := h.mover()
	if err != nil {
		return nil, err
	}
	comp, err := mv.Component(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("move %s: %w", req.FigaroID, err)
	}
	ids := comp.TrunkIDs()
	for _, id := range ids {
		if err := h.allowed(ctx, rpc.VerbKill, authz.Aria(id)); err != nil {
			return nil, fmt.Errorf("move %s: the fork tree holds an aria this caller may not kill: %w", req.FigaroID, err)
		}
	}
	if err := h.moving.claim(ids, to); err != nil {
		return nil, fmt.Errorf("move %s: %w", req.FigaroID, err)
	}
	settled := false
	defer func() {
		if !settled {
			h.moving.release(ids)
		}
	}()
	if err := h.quiesce(ids); err != nil {
		return nil, fmt.Errorf("move %s refused: %w", req.FigaroID, err)
	}
	start := time.Now()
	token := moveToken()
	out, err := mv.ExportComponent(req.FigaroID, token)
	exported := time.Now()
	if err != nil {
		return nil, fmt.Errorf("move %s: export: %w", req.FigaroID, err)
	}
	defer out.Close()
	if !sameIDs(out.TrunkIDs(), ids) {
		return nil, fmt.Errorf("move %s: the fork tree changed while it was quiesced; try again", req.FigaroID)
	}
	link, done, err := h.linkFor(ctx, to)
	if err != nil {
		return nil, fmt.Errorf("move %s: %w", req.FigaroID, err)
	}
	defer done()
	dep := store.Departure{Token: token, To: to, AtMS: start.UnixMilli(), Component: out.Component, Facts: h.movedFacts(ids)}
	if err := mv.Moves().Depart(dep); err != nil {
		return nil, err
	}
	sent, err := h.ship(ctx, link, token, out)
	shippedAt := time.Now()
	if err != nil {
		h.unwind(ctx, link, token, sent.begun)
		return nil, fmt.Errorf("move %s: %w", req.FigaroID, refusedBy(to, err))
	}
	commit, _ := json.Marshal(rpc.ImportCommitRequest{Token: token, Digest: sent.digest, Metas: out.Metas})
	if _, err := link.Call(ctx, rpc.MethodImportCommit, commit); err != nil {
		var answered *jkrpc.Error
		if errors.As(err, &answered) {
			h.unwind(ctx, link, token, true)
			return nil, fmt.Errorf("move %s: %w", req.FigaroID, refusedBy(to, err))
		}
		settled = true
		h.ensureSettling()
		return nil, fmt.Errorf("move %s: node %s did not answer the commit (%v); its arias answer as moving until node %s is asked again", req.FigaroID, to, err, to)
	}
	committed := time.Now()
	if err := h.arrive(token); err != nil {
		settled = true
		h.ensureSettling()
		return nil, fmt.Errorf("move %s: node %s holds it, and this node failed to let go: %w", req.FigaroID, to, err)
	}
	slog.Info("moved arias", "to", to, "ids", ids, "bytes", sent.bytes, "elapsed", time.Since(start),
		"export", exported.Sub(start), "ship", shippedAt.Sub(exported), "commit", committed.Sub(shippedAt), "let_go", time.Since(committed))
	return rpc.MoveResponse{Node: to, Moved: ids, Bytes: sent.bytes, Millis: time.Since(start).Milliseconds()}, nil
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

type queueReader interface {
	QueuedPrompts(carriers bool) (string, []rpc.QueuedPrompt)
}

func (h *handlers) quiesce(ids []string) error {
	h.restoreMu.Lock()
	for _, id := range ids {
		if _, waking := h.restoring[id]; waking {
			h.restoreMu.Unlock()
			return fmt.Errorf("%s is waking", id)
		}
	}
	h.restoreMu.Unlock()
	for _, id := range ids {
		f := h.angelus.Registry.Get(id)
		if f == nil {
			continue
		}
		if f.Info().State != "idle" {
			return fmt.Errorf("%s has a turn in flight", id)
		}
		if q, ok := f.(queueReader); ok {
			if _, queued := q.QueuedPrompts(false); len(queued) > 0 {
				return fmt.Errorf("%s has %d queued prompt(s)", id, len(queued))
			}
		}
	}
	for _, id := range ids {
		if h.angelus.Registry.Get(id) == nil {
			continue
		}
		if err := h.angelus.Registry.Hibernate(id); err != nil {
			return err
		}
	}
	return nil
}

func (h *handlers) movedFacts(ids []string) map[string]store.MovedFacts {
	out := make(map[string]store.MovedFacts, len(ids))
	for _, id := range ids {
		var f store.MovedFacts
		if m, err := h.angelus.Backend.Meta(id); err == nil && m != nil {
			f.MintedBy = m.MintedBy
		}
		seen := map[string]bool{id: true}
		for cur := id; ; {
			n, ok := h.angelus.Backend.Node(cur)
			if !ok || n.Parent == "" || seen[n.Parent] {
				break
			}
			f.Ancestors = append(f.Ancestors, n.Parent)
			seen[n.Parent] = true
			cur = n.Parent
		}
		out[id] = f
	}
	return out
}

func (h *handlers) sandboxesOf(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		kind := ""
		if snap, err := h.angelus.Backend.FormState(id); err == nil {
			if s := snap.Lookup("system.sandbox"); s != nil {
				kind = *s
			}
		}
		out[id] = kind
	}
	return out
}

type shipped struct {
	begun  bool
	bytes  int64
	digest string
}

func refusedBy(node string, err error) error {
	var answered *jkrpc.Error
	if errors.As(err, &answered) {
		return fmt.Errorf("node %s refused it: %s", node, answered.Message)
	}
	return fmt.Errorf("node %s: %w", node, err)
}

func (h *handlers) ship(ctx context.Context, link *peer.Link, token string, out *store.Outbound) (shipped, error) {
	comp, err := json.Marshal(out.Component)
	if err != nil {
		return shipped{}, err
	}
	begin, _ := json.Marshal(rpc.ImportBeginRequest{Token: token, Component: comp, Sandboxes: h.sandboxesOf(out.TrunkIDs())})
	if _, err := link.Call(ctx, rpc.MethodImportBegin, begin); err != nil {
		var answered *jkrpc.Error
		return shipped{begun: !errors.As(err, &answered)}, err
	}
	sent := shipped{begun: true}
	var digest xwal.Digest
	buf := make([]byte, rpc.MaxImportChunk)
	for _, f := range out.Files {
		sum, n, err := h.shipFile(ctx, link, token, f, buf)
		if err != nil {
			return sent, fmt.Errorf("%s/%s/%s: %w", f.Channel, f.Node, f.Name, err)
		}
		digest.Add(f.Node, f.Channel, f.Name, n, sum)
		sent.bytes += n
	}
	sent.digest = digest.Sum()
	return sent, nil
}

func (h *handlers) shipFile(ctx context.Context, link *peer.Link, token string, f xwal.ComponentFile, buf []byte) ([32]byte, int64, error) {
	var sum [32]byte
	r, err := f.Open()
	if err != nil {
		return sum, 0, err
	}
	defer r.Close()
	hash := sha256.New()
	var off int64
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 || off == 0 {
			hash.Write(buf[:n])
			chunk, _ := json.Marshal(rpc.ImportChunkRequest{Token: token, Node: f.Node, Channel: f.Channel, Name: f.Name, Offset: off, Data: buf[:n]})
			if _, err := link.Call(ctx, rpc.MethodImportChunk, chunk); err != nil {
				return sum, 0, err
			}
			off += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return sum, 0, rerr
		}
	}
	copy(sum[:], hash.Sum(nil))
	return sum, off, nil
}

func (h *handlers) unwind(ctx context.Context, link *peer.Link, token string, begun bool) {
	mv, err := h.mover()
	if err != nil {
		return
	}
	if begun {
		abort, _ := json.Marshal(rpc.ImportTokenRequest{Token: token})
		if _, err := link.Call(ctx, rpc.MethodImportAbort, abort); err != nil {
			slog.Warn("move: the new home did not take the abort; it will be asked again", "token", token, "err", err)
			if err := mv.Moves().Abort(token); err != nil {
				slog.Error("move: departure not marked aborted", "token", token, "err", err)
			}
			h.ensureSettling()
			return
		}
	}
	if err := mv.Moves().Cancel(token); err != nil {
		slog.Warn("move: departure not cancelled", "token", token, "err", err)
	}
}

func (h *handlers) arrive(token string) error {
	mv, err := h.mover()
	if err != nil {
		return err
	}
	dep, ok := mv.Moves().Departure(token)
	if !ok {
		return fmt.Errorf("no departure %s", token)
	}
	if err := mv.Moves().Settle(token); err != nil {
		return err
	}
	return h.letGo(mv, dep)
}

func (h *handlers) letGo(mv mover, dep store.Departure) error {
	ids := dep.Component.TrunkIDs()
	for _, id := range ids {
		if h.angelus.Sessions != nil {
			h.angelus.Sessions.KillScope(id)
		}
	}
	if h.holdsAny(ids) {
		if err := mv.RemoveMoved(dep.Component, dep.Token); err != nil {
			return fmt.Errorf("the old home still holds the moved arias: %w", err)
		}
	}
	h.moving.release(ids)
	for _, id := range ids {
		if hb := h.angelus.Hubs.drop(id); hb != nil {
			hb.Close()
		}
		if h.fed != nil {
			h.fed.dir.Learn(id, dep.To)
		}
	}
	go h.announce(ids, dep.To)
	return nil
}

func (h *handlers) holdsAny(ids []string) bool {
	for _, id := range ids {
		if _, ok := h.angelus.Backend.Node(id); ok {
			return true
		}
	}
	return false
}

func (h *handlers) announce(ids []string, node string) {
	if h.fed == nil {
		return
	}
	body, _ := json.Marshal(rpc.NodeLearnRequest{IDs: ids, Node: node})
	for _, peerNode := range h.fed.members.Nodes() {
		if peerNode == node {
			continue
		}
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		l, err := h.fed.dialer.Open(ctx, peerNode, rpc.Passage{}, nil)
		if err == nil {
			_, err = l.Call(ctx, rpc.MethodNodeLearn, body)
			l.Close()
		}
		cancel()
		if err != nil {
			slog.Info("move: a peer did not learn the new home", "peer", peerNode, "err", err)
		}
	}
}

func (h *handlers) settleDeparture(ctx context.Context, token string) {
	mv, err := h.mover()
	if err != nil {
		return
	}
	dep, ok := mv.Moves().Departure(token)
	if !ok || dep.State == store.MoveFinal {
		return
	}
	state, err := h.importState(ctx, dep.To, token, true)
	if err != nil {
		slog.Warn("move: the new home could not be asked; the move stays pending", "token", token, "to", dep.To, "err", err)
		return
	}
	switch {
	case dep.State == store.MoveAborted:
		if err := mv.Moves().Cancel(token); err != nil {
			slog.Error("move: forget an aborted departure", "token", token, "err", err)
		}
	case state == rpc.ImportCommitted:
		if err := h.arrive(token); err != nil {
			slog.Error("move: settle", "token", token, "err", err)
		}
	default:
		if err := mv.Moves().Cancel(token); err != nil {
			slog.Error("move: cancel", "token", token, "err", err)
			return
		}
		h.moving.release(dep.Component.TrunkIDs())
	}
}

func (h *handlers) importState(ctx context.Context, node, token string, abandon bool) (rpc.ImportState, error) {
	if h.fed == nil {
		return "", errors.New("no peers")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	l, err := h.fed.dialer.Open(ctx, node, rpc.Passage{}, nil)
	if err != nil {
		return "", err
	}
	defer l.Close()
	body, _ := json.Marshal(rpc.ImportStatusRequest{Token: token, Abandon: abandon})
	raw, err := l.Call(ctx, rpc.MethodImportStatus, body)
	if err != nil {
		return "", err
	}
	var resp rpc.ImportStatusResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", err
	}
	return resp.State, nil
}

func (h *handlers) holdDepartures() bool {
	mv, err := h.mover()
	if err != nil {
		return false
	}
	held := false
	for _, dep := range mv.Moves().Departures() {
		ids := dep.Component.TrunkIDs()
		if dep.State == store.MoveAborted || dep.State == store.MoveFinal && !h.holdsAny(ids) {
			held = held || dep.State == store.MoveAborted
			continue
		}
		if err := h.moving.claim(ids, dep.To); err != nil {
			slog.Error("move: departure overlaps another", "token", dep.Token, "err", err)
			continue
		}
		held = true
	}
	return held
}

func (h *handlers) settleDepartures(ctx context.Context) {
	mv, err := h.mover()
	if err != nil {
		return
	}
	for _, dep := range mv.Moves().Departures() {
		switch {
		case dep.State != store.MoveFinal:
			h.settleDeparture(ctx, dep.Token)
		case h.holdsAny(dep.Component.TrunkIDs()):
			if err := h.letGo(mv, dep); err != nil {
				slog.Warn("move: let go", "token", dep.Token, "err", err)
			}
		}
	}
}

func (h *handlers) unsettled() bool {
	mv, err := h.mover()
	if err != nil {
		return false
	}
	for _, dep := range mv.Moves().Departures() {
		if dep.State != store.MoveFinal {
			return true
		}
	}
	return false
}

func (h *handlers) ensureSettling() {
	if !h.settling.CompareAndSwap(false, true) {
		return
	}
	ctx := h.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer h.settling.Store(false)
		h.settleLoop(ctx)
	}()
}

func (h *handlers) settleLoop(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		h.settleDepartures(ctx)
		if h.moving.n.Load() == 0 && !h.unsettled() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (h *handlers) tombstone(id string) (store.Tombstone, bool) {
	if h.moves == nil || !h.moves.Any() {
		return store.Tombstone{}, false
	}
	return h.moves.Tombstone(id)
}

func (h *handlers) tombstoneByPrefix(ctx context.Context, prefix string) (store.Tombstone, bool) {
	if h.moves == nil || !h.moves.Any() {
		return store.Tombstone{}, false
	}
	mv := h.mv
	var found []store.Tombstone
	for _, id := range mv.Moves().MatchTombstones(prefix, -1) {
		t, ok := mv.Moves().Tombstone(id)
		if ok && h.allowed(ctx, rpc.VerbRead, authz.Aria(id)) == nil {
			found = append(found, t)
		}
	}
	if len(found) != 1 {
		return store.Tombstone{}, false
	}
	return found[0], true
}

func (h *handlers) towardsMoved(ctx context.Context, t store.Tombstone, route bool) (string, string, error) {
	if t.State != store.MoveFinal {
		return "", "", errMoving(t.ID, t.To)
	}
	if h.allowed(ctx, rpc.VerbRead, authz.Aria(t.ID)) != nil {
		return "", "", fmt.Errorf("%s: %w", t.ID, peer.ErrNoOwner)
	}
	if forwarded(ctx) {
		data, _ := json.Marshal(rpc.Moved{ID: t.ID, Node: t.To})
		return "", "", &jkrpc.Error{Code: rpc.ErrCodeMoved, Message: fmt.Sprintf("%s moved to node %s", t.ID, t.To), Data: data}
	}
	if !route {
		return "", "", fmt.Errorf("%s moved to node %s; this call is served only where it is made", t.ID, t.To)
	}
	to, ok := h.peerNamed(t.To)
	if !ok {
		return "", "", fmt.Errorf("%s moved to node %s, which this node does not peer with", t.ID, t.To)
	}
	if _, ok := authz.BearerOf(ctx); !ok {
		return "", "", h.needsBearer(t.ID, to)
	}
	return t.ID, to, nil
}
