package angelus_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/workerpool"
)

const twoPoolConfig = `
[worker.pools.groups]
enabled = true
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/groups/say"
workers = ["slot0"]

[worker.pools.others]
enabled = true
outfit = "leased"
provider = "mock"
rou2_lease_class = "group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/others/say"
workers = ["slot5"]
`

const brokerPoolPolicy = `
[roles.owner]
permissions = ["*:*"]

[roles.broker-groups]
permissions = ["lease:pool:groups"]

[[bind]]
role = "owner"
principals = ["user:SELF"]

[[bind]]
role = "broker-groups"
principals = ["user:broker"]
`

func asBroker() context.Context { return as("user", "broker") }

func pagesRig(t *testing.T) grantRig {
	t.Helper()
	r := newRigConfigured(t, brokerPoolPolicy, twoPoolConfig)
	workers, err := angelus.OpenWorkerPools(t.TempDir(), r.a.Settings)
	if err != nil {
		t.Fatal(err)
	}
	r.a.Workers = workers
	return r
}

func (r grantRig) pages(t *testing.T, ctx context.Context, params string) ([]rpc.WorkerPage, error) {
	t.Helper()
	out, err := r.h[rpc.MethodWorkerPages](ctx, json.RawMessage(params))
	if err != nil {
		return nil, err
	}
	resp, ok := out.(rpc.WorkerPagesResponse)
	if !ok {
		t.Fatalf("%s answered %T", rpc.MethodWorkerPages, out)
	}
	return resp.Pages, nil
}

func pageIDs(pages []rpc.WorkerPage) []uint64 {
	ids := make([]uint64, 0, len(pages))
	for _, p := range pages {
		ids = append(ids, p.ID)
	}
	return ids
}

// broker forwards a page and only then names it. Until its exact id is
// named the pool answers it on every call, so a page broker failed to
// forward is not lost behind a later one.
func TestAPageIsAnsweredOnEveryCallUntilItsIDIsNamed(t *testing.T) {
	r := pagesRig(t)
	for _, kind := range []workerpool.PageKind{workerpool.PurgeFailed, workerpool.SuccessionFailed, workerpool.LeaseClosed} {
		if _, err := r.a.Workers.Ledger.Page("groups", "chat:10", 1, kind, "ab12"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.a.Workers.Ledger.Page("others", "chat:77", 1, workerpool.PurgeFailed, "cd34"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		params string
		want   []uint64
	}{
		{`{"pool":"groups"}`, []uint64{1, 2, 3}},
		{`{"pool":"groups"}`, []uint64{1, 2, 3}},
		{`{"pool":"groups","seen":[3]}`, []uint64{1, 2}},
		{`{"pool":"groups","seen":[3,99]}`, []uint64{1, 2}},
		{`{"pool":"groups","seen":[1,2]}`, []uint64{}},
		{`{"pool":"groups"}`, []uint64{}},
	} {
		got, err := r.pages(t, asBroker(), c.params)
		if err != nil || !slices.Equal(pageIDs(got), c.want) {
			t.Fatalf("%s: pages %v (%v), want %v", c.params, pageIDs(got), err, c.want)
		}
	}
	held, err := r.pages(t, asOwner(), `{"pool":"others"}`)
	if err != nil || len(held) != 1 {
		t.Fatalf("the other pool's page after groups was acknowledged: %v %v, want it still held", held, err)
	}
	raw, _ := json.Marshal(held[0])
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := []string{"id", "at", "pool", "key", "generation", "kind", "figaro", "dropped"}
	for name := range fields {
		if !slices.Contains(allowed, name) {
			t.Errorf("a page carries %q; it carries ids and a kind and nothing else", name)
		}
	}
	if fields["kind"] != "purge_failed" || fields["key"] != "chat:77" || fields["figaro"] != "cd34" {
		t.Errorf("the page broker reads: %s", raw)
	}
}

// What the method refuses. Each row is something broker, a stranger or
// a confused caller could send; none may read or acknowledge a page.
func TestPagesRefusesEveryCallItWasNotBuiltFor(t *testing.T) {
	r := pagesRig(t)
	if _, err := r.a.Workers.Ledger.Page("groups", "chat:10", 1, workerpool.PurgeFailed, "ab12"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.a.Workers.Ledger.Page("others", "chat:77", 1, workerpool.PurgeFailed, "cd34"); err != nil {
		t.Fatal(err)
	}
	throughNodeDoor := func(ctx context.Context) context.Context { return authz.WithDoor(ctx, authz.DoorNode) }
	for name, c := range map[string]struct {
		ctx    context.Context
		params string
	}{
		"a wait over 50":                {asBroker(), `{"pool":"groups","seen":[1],"wait":51}`},
		"a negative wait":               {asBroker(), `{"pool":"groups","seen":[1],"wait":-1}`},
		"a wait that wraps a duration":  {asBroker(), `{"pool":"groups","seen":[1],"wait":18446744074}`},
		"a field it does not know":      {asBroker(), `{"pool":"groups","seen":[1],"outfit":"x"}`},
		"a route":                       {asBroker(), `{"pool":"groups","seen":[1],"page_route":"gluck"}`},
		"a figaro id":                   {asBroker(), `{"pool":"groups","seen":[1],"figaro_id":"ab12"}`},
		"through N in place of ids":     {asBroker(), `{"pool":"groups","seen":1}`},
		"two values":                    {asBroker(), `{"pool":"groups","seen":[1]} {"pool":"groups"}`},
		"another pool":                  {asBroker(), `{"pool":"others","seen":[1]}`},
		"no pool":                       {asBroker(), `{"seen":[1]}`},
		"no params":                     {asBroker(), ``},
		"a stranger":                    {as("user", "mallory"), `{"pool":"groups","seen":[1]}`},
		"broker through a node door":    {throughNodeDoor(asBroker()), `{"pool":"groups","seen":[1]}`},
		"the owner through a node door": {throughNodeDoor(asOwner()), `{"pool":"groups","seen":[1]}`},
		"a pool the config lacks":       {asOwner(), `{"pool":"nowhere","seen":[1]}`},
	} {
		if pages, err := r.pages(t, c.ctx, c.params); err == nil {
			t.Errorf("%s: answered %v", name, pageIDs(pages))
		}
	}
	for _, pool := range []string{"groups", "others"} {
		if held, err := r.pages(t, asOwner(), `{"pool":"`+pool+`"}`); err != nil || len(held) != 1 {
			t.Errorf("after the refused calls %s holds %v (%v), want its one page: a refused call acknowledged it", pool, pageIDs(held), err)
		}
	}
	for _, method := range []string{rpc.MethodKill, rpc.MethodCreate, rpc.MethodDecide, rpc.MethodStatus} {
		if _, err := r.h[method](asBroker(), json.RawMessage(`{"pool":"groups"}`)); err == nil {
			t.Errorf("lease:pool:groups let broker call %s", method)
		}
	}
}

// A daemon with no pools has no ledger, and the method says so to the
// owner too, whose *:* now covers lease.
func TestADaemonThatLeasesNothingRefusesPagesForItsOwner(t *testing.T) {
	r := newGrantRig(t)
	_, err := r.pages(t, asOwner(), `{"pool":"groups"}`)
	if err == nil || !strings.Contains(err.Error(), "leases no slots") {
		t.Fatalf("pages on a daemon without pools: %v", err)
	}
}

// wait is in seconds. A call with nothing held stays open until a page
// is recorded; were the number read as a smaller unit it would answer
// empty before the page below exists.
func TestPagesWaitsInSecondsForTheNextPage(t *testing.T) {
	r := pagesRig(t)
	type answer struct {
		pages []rpc.WorkerPage
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		pages, err := r.pages(t, asBroker(), `{"pool":"groups","wait":50}`)
		done <- answer{pages, err}
	}()
	select {
	case a := <-done:
		t.Fatalf("with nothing held a waiting call answered at once: %v %v", a.pages, a.err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := r.a.Workers.Ledger.Page("groups", "chat:10", 1, workerpool.SuccessionBound, "ab12"); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-done:
		if a.err != nil || !slices.Equal(pageIDs(a.pages), []uint64{1}) {
			t.Fatalf("the waiting call answered %v (%v), want the page just recorded", pageIDs(a.pages), a.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting call did not answer when a page was recorded")
	}
}
