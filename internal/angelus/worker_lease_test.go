package angelus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/workerpool"
)

const leasingConfig = `
[worker.pools.groups]
enabled = true
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/groups/say"
search_url = "http://127.0.0.1:4390/search"
search_credential = "search-groups"
workers = ["worker0", "worker1"]

[worker.pools.others]
enabled = true
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/others/say"
workers = ["worker5"]

[worker.pools.closed]
enabled = false
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/closed/say"
workers = ["worker9"]
`

const leasingPolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.broker-groups]
permissions = ["lease:pool:groups", "lease:pool:closed"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "broker-groups"
principals = ["user:broker", "user:rival"]
`

func leasingRig(t *testing.T) grantRig {
	t.Helper()
	return leasingRigUnder(t, leasingPolicy)
}

func leasingRigUnder(t *testing.T, policy string) grantRig {
	t.Helper()
	r := newRigProviding(t, policy, leasingConfig, func(_ string, knobs provider.Knobs) (provider.Provider, error) {
		return &leasedMock{lease: knobs.Lease}, nil
	}, thatWakesFigaros)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	r.writeLeasedOutfit(t, leasedOutfit)
	workers, err := angelus.OpenWorkerPools(t.TempDir(), r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	r.a.Workers = workers
	r.a.Policies.SetLeases(workers)
	r.a.Policies.SetPoolDaemon(true)
	installRou2(r)
	return r
}

func (r grantRig) writeLeasedOutfit(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.policy), "outfits", "leased.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h[rpc.MethodConfigure](asOwner(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func (r grantRig) lease(ctx context.Context, params string) (rpc.WorkerLeaseResponse, error) {
	out, err := r.h[rpc.MethodWorkerLease](ctx, json.RawMessage(params))
	if err != nil {
		return rpc.WorkerLeaseResponse{}, err
	}
	return out.(rpc.WorkerLeaseResponse), nil
}

func (r grantRig) release(ctx context.Context, params string) error {
	_, err := r.h[rpc.MethodWorkerRelease](ctx, json.RawMessage(params))
	return err
}

func workerRefusal(t *testing.T, err error) rpc.WorkerRefusal {
	t.Helper()
	var coded *jkrpc.Error
	if !errors.As(err, &coded) || coded.Code != rpc.ErrCodeWorker {
		t.Fatalf("want a worker refusal (%d), got %v", rpc.ErrCodeWorker, err)
	}
	var out rpc.WorkerRefusal
	if err := json.Unmarshal(coded.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The lease, through the guard, as broker: a box of the next generation
// of a free worker, a figaro in it, its role, a bearer; the same answer
// again for the same key; the pool's bound; and nothing for a caller that
// does not hold the key or the pool.
func TestALeaseAnswersItsBoxFigaroRoleAndBearerAndTheSameAgain(t *testing.T) {
	r := leasingRig(t)
	first, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	if first.Pool != "groups" || first.Key != "chat:10" || first.Generation != 1 || first.Box != "worker0-g1" {
		t.Errorf("the lease is %+v, want generation 1 of worker0 for groups/chat:10", first)
	}
	if got := r.sandboxOf(t, first.Aria); got != first.Box {
		t.Errorf("the leased figaro runs in %q, want %q", got, first.Box)
	}
	if target := string(r.formOf(t, first.Role)["target-aria"]); target != `"`+first.Aria+`"` {
		t.Errorf("the role points at %s, want the figaro %s", target, first.Aria)
	}
	if raw, err := json.Marshal(first.SayBearer); err != nil || len(first.SayBearer) != 64 || strings.Trim(first.SayBearer, "0123456789abcdef") != "" {
		t.Errorf("the say bearer is %s, want 256 bits in hex", raw)
	}
	row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1)
	if row.State != workerpool.Live || row.Holder != "user:broker" || row.SayBearer != first.SayBearer {
		t.Errorf("the ledger row is %+v, want it live, broker's, with the bearer", row.WithoutBearer())
	}
	if boxes := r.a.Settings.LeasedBoxes(); !slices.Equal(boxes, []string{"worker0-g1"}) {
		t.Errorf("the leased boxes are %v", boxes)
	}

	before := r.nodeIDs()
	again, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil || again != first {
		t.Errorf("the same key leased again answers %+v (%v), want the first answer", again, err)
	}
	if after := r.nodeIDs(); !slices.Equal(after, before) {
		t.Error("the second answer minted something")
	}

	// With a worker still free, so that each of these is refused for what it is.
	for why, params := range map[string]string{
		"an outfit beside the key": `{"pool":"groups","key":"chat:13","outfits":["mock"]}`,
		"a key outside the syntax": `{"pool":"groups","key":"chat 13"}`,
		"no key":                   `{"pool":"groups"}`,
		"a pool broker holds not":  `{"pool":"others","key":"chat:13"}`,
	} {
		resp, err := r.lease(asBroker(), params)
		if err == nil {
			t.Errorf("%s: leased %+v", why, resp)
		} else if strings.Contains(err.Error(), rpc.WorkerPoolEmpty) || strings.Contains(err.Error(), "no free worker") {
			t.Errorf("%s: refused only for want of a worker: %v", why, err)
		}
	}
	if _, err := r.lease(as("user", "rival"), `{"pool":"groups","key":"chat:10"}`); err == nil || workerRefusal(t, err).Reason != rpc.WorkerNoSuchLease {
		t.Errorf("another holder of the pool asked for broker's key: %v", err)
	}
	second, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:11"}`)
	if err != nil || second.Box != "worker1-g1" || second.Aria == first.Aria || second.Role == first.Role || second.SayBearer == first.SayBearer {
		t.Errorf("a second key leases %+v (%v), want worker1 with its own figaro, role and bearer", second, err)
	}
	_, err = r.lease(asBroker(), `{"pool":"groups","key":"chat:12"}`)
	if err == nil {
		t.Fatal("a third key was leased from a pool of two workers")
	}
	if got, want := workerRefusal(t, err), (rpc.WorkerRefusal{Reason: rpc.WorkerPoolEmpty, Pool: "groups", Workers: 2, Leased: 2}); got != want {
		t.Errorf("the empty pool answers %+v, want %+v", got, want)
	}

	if _, err := r.lease(asBroker(), `{"pool":"closed","key":"chat:13"}`); err == nil || workerRefusal(t, err).Reason != rpc.WorkerPoolDisabled {
		t.Errorf("a disabled pool leased: %v", err)
	}
	if n := len(r.a.Workers.Ledger.Leases()); n != 2 {
		t.Errorf("the ledger holds %d rows after the refusals, want the two leases", n)
	}
}

// A lease whose mint fails goes through the release path before the
// caller hears of it: nothing of it is in the store or the box table, its
// row is released, and the worker is offered again at the next generation.
func TestALeaseThatCannotMintLeavesNothingAndItsWorkerFree(t *testing.T) {
	r := leasingRig(t)
	path := filepath.Join(filepath.Dir(r.policy), "outfits", "leased.toml")
	edit := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.h[rpc.MethodOutfitReload](asOwner(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	edit(strings.Replace(leasedOutfit, "max_tokens = 4096", "max_tokens = 9", 1))
	before := r.nodeIDs()
	if resp, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`); err == nil || !strings.Contains(err.Error(), "changed since its pool loaded") {
		t.Fatalf("an outfit edited since the pool loaded was minted: %+v, %v", resp, err)
	}
	if after := r.nodeIDs(); !slices.Equal(after, before) {
		t.Errorf("the failed lease left the store at %v, was %v", after, before)
	}
	if boxes := r.a.Settings.LeasedBoxes(); len(boxes) != 0 {
		t.Errorf("the failed lease left the boxes %v", boxes)
	}
	if row, ok := r.a.Workers.Ledger.Find("groups", "chat:10", 1); !ok || row.State != workerpool.Released {
		t.Errorf("the failed lease's row is %+v, want it released", row)
	}
	edit(leasedOutfit)
	next, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil || next.Box != "worker0-g2" {
		t.Errorf("after the failure the key leases %+v (%v), want worker0 at generation 2", next, err)
	}
}

// Release deletes what the lease minted and whatever else carries its
// box, takes the box out of the table and frees the worker. It needs the
// generation and the holder, answers a second time as the first, and
// touches nothing of another lease or of the owner.
func TestAReleaseDeletesEverythingOfTheLeaseAndFreesItsWorker(t *testing.T) {
	r := leasingRig(t)
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:11"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.policy), "outfits", "boxed.toml"), []byte("[system]\nprovider = \"mock\"\nmodel = \"m\"\nsandbox = \""+lease.Box+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stray, err := r.create(asOwner(), "boxed")
	if err != nil {
		t.Fatal(err)
	}
	owners, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	strayForm := r.formIn(t, lease.Box)
	node, _ := r.hs.PoolNode("groups")

	for why, call := range map[string]func() error{
		"with no generation":      func() error { return r.release(asBroker(), `{"pool":"groups","key":"chat:10"}`) },
		"with another generation": func() error { return r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":2}`) },
		"by another holder": func() error {
			return r.release(as("user", "rival"), `{"pool":"groups","key":"chat:10","generation":1}`)
		},
		"with a field it does not take": func() error {
			return r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1,"seen":[1]}`)
		},
	} {
		if err := call(); err == nil {
			t.Errorf("a release %s was served", why)
		}
		if !ariaExists(r.a, lease.Aria) {
			t.Fatalf("a release %s deleted the figaro", why)
		}
	}

	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{"the figaro": lease.Aria, "the role": lease.Role, "a figaro the owner bore in the box": stray.FigaroID, "a form held in the box that no row names": strayForm} {
		if ariaExists(r.a, id) {
			t.Errorf("%s outlived the release", name)
		}
	}
	for name, id := range map[string]string{"the other lease's figaro": other.Aria, "the other lease's role": other.Role, "the owner's figaro": owners.FigaroID, "the pool's node": node} {
		if !ariaExists(r.a, id) {
			t.Errorf("the release deleted %s", name)
		}
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1); row.State != workerpool.Released || row.SayBearer != "" || len(row.Minted) != 0 {
		t.Errorf("the released row is %+v", row)
	}
	if boxes := r.a.Settings.LeasedBoxes(); !slices.Equal(boxes, []string{other.Box}) {
		t.Errorf("the leased boxes after the release are %v, want only %s", boxes, other.Box)
	}
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Errorf("a second release of the same generation: %v", err)
	}
	next, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil || next.Box != "worker0-g2" || next.SayBearer == lease.SayBearer {
		t.Errorf("after the release the key leases %+v (%v), want worker0 at generation 2 with a new bearer", next, err)
	}
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil || !ariaExists(r.a, next.Aria) {
		t.Errorf("a late release of generation 1 touched generation 2 (%v)", err)
	}
}

func (r grantRig) formIn(t *testing.T, box string) string {
	t.Helper()
	out, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"system.sandbox": box, "note": "held"})})
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.FormCreateResponse).FormID
}

// What a start does before any endpoint serves (El Inspector XV, C4). A lease
// the last run left minting is released, with what it had minted. A node
// that carries a leased box's name and that no lease holds, which is what
// a crash between a birth and its ledger write leaves, is purged. A live
// lease, the pools' nodes and a form in a box that is no worker's keep.
func TestAStartReleasesUnfinishedLeasesAndPurgesWhatNoLeaseHolds(t *testing.T) {
	r := leasingRig(t)
	live, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.a.Workers.Ledger.Begin("groups", "worker1", "chat:11", 1, "user:broker"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.a.Settings.LeaseBox("groups", "worker1", 1); err != nil {
		t.Fatal(err)
	}
	figaro, role, err := r.hs.MintLeased(asBroker(), "groups", "chat:11", 1)
	if err != nil {
		t.Fatal(err)
	}
	unnamed := r.formIn(t, "worker1-g1")
	orphan := r.formIn(t, "worker5-g3")
	elsewhere := r.formIn(t, "worker5-gx")
	inLive := r.formIn(t, live.Box)
	node, _ := r.hs.PoolNode("groups")

	r.hs.RecoverWorkerPools(context.Background())

	for name, id := range map[string]string{"the unfinished lease's figaro": figaro, "its role": role, "a form in its box that its row does not name": unnamed, "a form in a box no lease holds": orphan} {
		if ariaExists(r.a, id) {
			t.Errorf("%s outlived the start", name)
		}
	}
	for name, id := range map[string]string{"the live lease's figaro": live.Aria, "its role": live.Role, "a form in the live lease's box": inLive, "a form whose box is no worker's generation": elsewhere, "the pool's node": node} {
		if !ariaExists(r.a, id) {
			t.Errorf("the start deleted %s", name)
		}
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:11", 1); row.State != workerpool.Released {
		t.Errorf("the unfinished lease's row is %s, want released", row.State)
	}
	if row, _ := r.a.Workers.Ledger.Find("groups", "chat:10", 1); row.State != workerpool.Live {
		t.Errorf("the live lease's row is %s", row.State)
	}
	if next, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:12"}`); err != nil || next.Box != "worker1-g2" {
		t.Errorf("after the start the freed worker leases %+v (%v), want worker1 at generation 2", next, err)
	}
}

// The orphan purge at a start runs as the daemon's owner, whom the guard
// refuses nothing, so the purge keeps itself to leased boxes that no lease
// holds (El Inspector XV, O1). An orphan drawn above a node of no leased box
// is left where it is and logged; the node under it keeps. An orphan whose
// delete takes only orphans still goes.
func TestAStartLeavesAnOrphanWhoseDeleteWouldTakeWhatIsNoOrphan(t *testing.T) {
	r := leasingRig(t)
	orphan := r.formIn(t, "worker5-g3")
	out, err := r.call(asOwner(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: heldPatch(t, map[string]any{"note": "the owner's"})})
	if err != nil {
		t.Fatal(err)
	}
	unboxed := out.(rpc.FormCreateResponse).FormID
	if err := r.hs.Place(asOwner(), unboxed, orphan); err != nil {
		t.Fatal(err)
	}
	above, below := r.formIn(t, "worker5-g3"), r.formIn(t, "worker5-g4")
	if err := r.hs.Place(asOwner(), below, above); err != nil {
		t.Fatal(err)
	}

	var logged bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	r.hs.RecoverWorkerPools(context.Background())
	slog.SetDefault(old)

	if !ariaExists(r.a, unboxed) {
		t.Error("the start deleted a node of no leased box, drawn under an orphan")
	}
	if !ariaExists(r.a, orphan) {
		t.Error("the start purged an orphan whose delete was refused")
	}
	left := ""
	for _, line := range strings.Split(logged.String(), "\n") {
		if strings.Contains(line, "level=ERROR") && strings.Contains(line, "node="+orphan) {
			left = line
		}
	}
	if !strings.Contains(left, unboxed) {
		t.Errorf("the orphan left in place is not logged with the node its delete would take: %q", logged.String())
	}
	for name, id := range map[string]string{"an orphan drawn above an orphan": above, "the orphan under it": below} {
		if ariaExists(r.a, id) {
			t.Errorf("%s outlived the start", name)
		}
	}
}
