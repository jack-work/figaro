package angelus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/workerpool"
)

const leaseRetryAfter = 5

func decodeExactly(params json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

func (h *handlers) workerPool(method, pool string) (*WorkerPools, error) {
	workers := h.angelus.Workers
	if workers == nil {
		return nil, fmt.Errorf("%s: this daemon leases no slots", method)
	}
	loaded, _ := h.settings()
	if _, ok := loaded.Pool(pool); !ok {
		return nil, fmt.Errorf("%s: no pool %q", method, pool)
	}
	return workers, nil
}

func (h *handlers) workerPages(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.WorkerPagesRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool, seen and wait and nothing else: %w", rpc.MethodWorkerPages, err)
	}
	workers, err := h.workerPool(rpc.MethodWorkerPages, req.Pool)
	if err != nil {
		return nil, err
	}
	if req.Wait < 0 || req.Wait > int(workerpool.MaxPageWait/time.Second) {
		return nil, fmt.Errorf("%s: %w, and wait was %d", rpc.MethodWorkerPages, workerpool.ErrPageWait, req.Wait)
	}
	held, err := workers.Ledger.Pages(ctx, req.Pool, req.Seen, time.Duration(req.Wait)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerPages, err)
	}
	pages := make([]rpc.WorkerPage, 0, len(held))
	for _, p := range held {
		pages = append(pages, rpc.WorkerPage{ID: p.ID, At: p.At, Pool: p.Pool, Key: p.Key, Generation: p.Generation, Kind: string(p.Kind), Figaro: p.Figaro, Dropped: p.Dropped})
	}
	return rpc.WorkerPagesResponse{Pages: pages}, nil
}

func workerRefused(data rpc.WorkerRefusal, format string, args ...any) error {
	raw, _ := json.Marshal(data)
	return &jkrpc.Error{Code: rpc.ErrCodeWorker, Message: fmt.Sprintf(format, args...), Data: raw}
}

func (h *handlers) leaseSeat(l workerpool.Lease) (aria, role string) {
	backend := h.angelus.Backend
	for _, id := range l.Minted {
		if n, ok := backend.Node(id); ok && n.Kind == store.KindForm {
			role = id
			break
		}
	}
	if snap, err := backend.FormState(role); role != "" && err == nil {
		if target := snap.Lookup("target-aria"); target != nil {
			aria = *target
		}
	}
	return aria, role
}

func (h *handlers) leaseAnswer(l workerpool.Lease) rpc.WorkerLeaseResponse {
	aria, role := h.leaseSeat(l)
	return rpc.WorkerLeaseResponse{Pool: l.Pool, Key: l.Key, Generation: l.Generation, Box: config.LeasedBoxName(l.Worker, l.Generation), Aria: aria, Role: role, SayBearer: l.SayBearer}
}

func freeWorker(pool config.PoolConfig, name string, leases []workerpool.Lease) (worker string, leased, releasing int) {
	taken := map[string]bool{}
	for _, l := range leases {
		if l.Pool != name || leaseIsOver(l) {
			continue
		}
		taken[l.Worker] = true
		if l.State == workerpool.Releasing {
			releasing++
		} else {
			leased++
		}
	}
	for _, s := range pool.WorkerNames() {
		if !taken[s] {
			return s, leased, releasing
		}
	}
	return "", leased, releasing
}

func (h *handlers) workerLease(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.WorkerLeaseRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool and key and nothing else: %w", rpc.MethodWorkerLease, err)
	}
	workers, err := h.workerPool(rpc.MethodWorkerLease, req.Pool)
	if err != nil {
		return nil, err
	}
	if err := workerpool.CheckKey(req.Key); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerLease, err)
	}
	loaded, _ := h.settings()
	pool, _ := loaded.Pool(req.Pool)
	if !pool.Enabled {
		return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerPoolDisabled, Pool: req.Pool}, "%s: pool %s is disabled", rpc.MethodWorkerLease, req.Pool)
	}
	holder := authz.PrincipalText(authz.PrincipalOf(ctx))
	workers.leasing.Lock()
	defer workers.leasing.Unlock()
	if held, ok := workers.Ledger.Held(req.Pool, req.Key); ok {
		switch {
		case held.Holder != holder:
			return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerNoSuchLease, Pool: req.Pool}, "%s: no such lease", rpc.MethodWorkerLease)
		case held.State == workerpool.Live:
			return h.leaseAnswer(held), nil
		}
		return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerReleasing, Pool: req.Pool, RetryAfter: leaseRetryAfter}, "%s: the key's last lease is still being released", rpc.MethodWorkerLease)
	}
	worker, leased, releasing := freeWorker(pool, req.Pool, workers.Ledger.Leases())
	if worker == "" {
		refusal := rpc.WorkerRefusal{Reason: rpc.WorkerPoolEmpty, Pool: req.Pool, Workers: len(pool.Workers), Leased: leased, Releasing: releasing}
		if releasing > 0 {
			refusal.RetryAfter = leaseRetryAfter
		}
		slog.Warn("slot pool: empty, a lease was refused", "pool", req.Pool, "slots", refusal.Workers, "leased", leased, "releasing", releasing)
		return nil, workerRefused(refusal, "%s: pool %s has no free slot", rpc.MethodWorkerLease, req.Pool)
	}
	generation, err := workers.Generations.Next(worker, workers.Ledger.Floor(worker))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerLease, err)
	}
	if err := workers.Ledger.Begin(req.Pool, worker, req.Key, generation, holder); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerLease, err)
	}
	live, err := h.bringLeaseLive(ctx, loaded, req.Pool, worker, req.Key, generation)
	if err != nil {
		if rerr := h.releaseLease(ctx, req.Pool, req.Key, generation); rerr != nil {
			slog.Error("slot pool: a lease that failed to mint is not yet released, and its slot is not offered", "pool", req.Pool, "slot", worker, "generation", generation, "err", rerr)
		}
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerLease, err)
	}
	return h.leaseAnswer(live), nil
}

func (h *handlers) leaseCaller(loaded *config.Loaded, pool string) (workerpool.Caller, string, error) {
	p, ok := loaded.Pool(pool)
	if !ok {
		return nil, "", fmt.Errorf("no pool %q", pool)
	}
	if h.angelus.Workers.Caller == nil {
		return nil, "", fmt.Errorf("pool %s: this daemon reaches no rou2 to open or close a lease's caller", pool)
	}
	caller, err := h.angelus.Workers.Caller(p)
	if err != nil {
		return nil, "", fmt.Errorf("pool %s: its provider %s reaches no rou2: %w", pool, p.Provider, err)
	}
	return caller, p.Rou2LeaseClass, nil
}

func (h *handlers) bringLeaseLive(ctx context.Context, loaded *config.Loaded, pool, worker, key string, generation uint64) (workerpool.Lease, error) {
	workers := h.angelus.Workers
	caller, class, err := h.leaseCaller(loaded, pool)
	if err != nil {
		return workerpool.Lease{}, err
	}
	rou2Lease := workerpool.CallerID(pool, config.LeasedBoxName(worker, generation))
	if err := workers.Ledger.Opens(pool, key, generation, rou2Lease); err != nil {
		return workerpool.Lease{}, err
	}
	if err := caller.Open(ctx, rou2Lease, class); err != nil {
		return workerpool.Lease{}, err
	}
	if _, err := loaded.LeaseBox(pool, worker, generation); err != nil {
		return workerpool.Lease{}, err
	}
	if _, err := h.mintLeased(ctx, pool, key, generation); err != nil {
		return workerpool.Lease{}, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return workerpool.Lease{}, fmt.Errorf("say bearer: %w", err)
	}
	if err := workers.Ledger.GoLive(pool, key, generation, hex.EncodeToString(secret[:])); err != nil {
		return workerpool.Lease{}, err
	}
	live, _ := workers.Ledger.Find(pool, key, generation)
	return live, nil
}

func (h *handlers) workerRelease(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.WorkerReleaseRequest
	if err := decodeExactly(params, &req); err != nil {
		return nil, fmt.Errorf("%s takes pool, key and generation and nothing else: %w", rpc.MethodWorkerRelease, err)
	}
	workers, err := h.workerPool(rpc.MethodWorkerRelease, req.Pool)
	if err != nil {
		return nil, err
	}
	workers.leasing.Lock()
	defer workers.leasing.Unlock()
	row, ok := workers.Ledger.Find(req.Pool, req.Key, req.Generation)
	if !ok || row.Holder != authz.PrincipalText(authz.PrincipalOf(ctx)) {
		return nil, workerRefused(rpc.WorkerRefusal{Reason: rpc.WorkerNoSuchLease, Pool: req.Pool}, "%s: no such lease", rpc.MethodWorkerRelease)
	}
	if leaseIsOver(row) {
		return rpc.WorkerReleaseResponse{Released: true}, nil
	}
	if err := h.releaseLease(ctx, req.Pool, req.Key, req.Generation); err != nil {
		return nil, fmt.Errorf("%s: %w", rpc.MethodWorkerRelease, err)
	}
	return rpc.WorkerReleaseResponse{Released: true}, nil
}
