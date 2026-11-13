package angelus_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

type brokerSays struct {
	mu     sync.Mutex
	status int
	got    []*http.Request
	bodies []string
}

func (h *brokerSays) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	h.got = append(h.got, r)
	h.bodies = append(h.bodies, string(body))
	status := h.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"said by broker, never to be quoted"}`)), Header: http.Header{}}, nil
}

func sayRig(t *testing.T) (grantRig, *brokerSays, rpc.SlotLeaseResponse) {
	t.Helper()
	return sayRigUnder(t, leasingPolicy)
}

func sayRigUnder(t *testing.T, policy string) (grantRig, *brokerSays, rpc.SlotLeaseResponse) {
	t.Helper()
	r := leasingRigUnder(t, policy)
	broker := &brokerSays{}
	r.a.Slots.SayClient = &http.Client{Transport: broker}
	lease, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:10"}`)
	if err != nil {
		t.Fatal(err)
	}
	return r, broker, lease
}

// A figaro woken in a leased box has the pool's tools and no other: no
// file or exec tool is registered for it, and search only where its pool
// names a search endpoint. A figaro in no box keeps the daemon's.
func TestAFigaroInALeasedBoxWakesWithThePoolsToolsAlone(t *testing.T) {
	r, _, lease := sayRig(t)
	if got := r.hs.ToolsAtWake(lease.Box, lease.Aria); !slices.Equal(got, []string{"say", "search"}) {
		t.Errorf("a leased figaro wakes with %v, want say and search", got)
	}
	if got := r.hs.ToolsAtWake("slot1-g4", "x"); !slices.Equal(got, []string{"say", "search"}) {
		t.Errorf("a figaro in a slot's generation no lease holds wakes with %v, want say and search", got)
	}
	if got := r.hs.ToolsAtWake("slot5-g1", "x"); !slices.Equal(got, []string{"say"}) {
		t.Errorf("a figaro of a pool with no search endpoint wakes with %v, want say alone", got)
	}
	if got := r.hs.ToolsAtWake("", "x"); !slices.Equal(got, []string{"read", "write"}) {
		t.Errorf("a figaro in no box wakes with %v, want the daemon's tools", got)
	}
}

// The tool says through the guard as the lease's principal, claiming its
// figaro, and the daemon posts the text to the pool's say URL with the
// lease's bearer, naming the key and the generation and nothing else.
func TestALeasedFigaroSaysToBrokerWithItsLeasesBearer(t *testing.T) {
	r, broker, lease := sayRig(t)
	if err := r.hs.SayAs(context.Background(), lease.Box, lease.Aria, "hello group"); err != nil {
		t.Fatal(err)
	}
	if len(broker.got) != 1 {
		t.Fatalf("broker was asked %d times", len(broker.got))
	}
	req := broker.got[0]
	if req.Method != http.MethodPost || req.URL.String() != "https://broker.example/groups/say" || req.Header.Get("Authorization") != "Bearer "+lease.SayBearer {
		t.Errorf("the say went as %s %s with %q", req.Method, req.URL, req.Header.Get("Authorization"))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(broker.bodies[0]), &body); err != nil || len(body) != 3 || body["key"] != "chat:10" || body["generation"] != float64(1) || body["text"] != "hello group" {
		t.Errorf("broker was sent %s", broker.bodies[0])
	}
}

// What a say is refused for, and that a refused one reaches no broker: a
// figaro that does not run in the lease's box, no figaro, a lease that is
// not live, a call through any door. A broker refusal is the tool's error,
// named by its status and not retried.
func TestASayIsRefusedForAnyoneButTheLeasesOwnFigaro(t *testing.T) {
	r, broker, lease := sayRig(t)
	other, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:11"}`)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := r.create(asOwner(), "mock")
	if err != nil {
		t.Fatal(err)
	}
	for why, figaro := range map[string]string{"another lease's figaro": other.Aria, "the owner's figaro": owners.FigaroID, "no figaro": "", "the lease's role": lease.Role} {
		if err := r.hs.SayAs(context.Background(), lease.Box, figaro, "t"); err == nil {
			t.Errorf("a say claiming %s was posted", why)
		}
	}
	if err := r.hs.SayAs(context.Background(), "slot1-g7", lease.Aria, "t"); err == nil {
		t.Error("a say from a box no lease holds was posted")
	}
	for why, ctx := range map[string]context.Context{"the owner": asOwner(), "broker": asBroker()} {
		params, _ := rpc.WithCaller(rpc.SlotSayRequest{Text: "t"}, lease.Aria, nil)
		if _, err := r.h[rpc.MethodSlotSay](ctx, params); err == nil {
			t.Errorf("%s said through a door", why)
		}
	}
	for _, door := range []authz.Door{authz.DoorBox, authz.DoorWeb, authz.DoorLocal} {
		params, _ := rpc.WithCaller(rpc.SlotSayRequest{Text: "t"}, lease.Aria, nil)
		if _, err := r.h[rpc.MethodSlotSay](angelus.AsLeasedBoxThrough(lease.Box, door), params); err == nil {
			t.Errorf("the lease's own principal said through the %v door", door)
		}
	}
	if len(broker.got) != 0 {
		t.Fatalf("refused says reached broker %d times", len(broker.got))
	}
	broker.status = http.StatusForbidden
	err = r.hs.SayAs(context.Background(), lease.Box, lease.Aria, "t")
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "said by broker") {
		t.Errorf("a say broker refused answered %v, want its status and not its body", err)
	}
	if len(broker.got) != 1 {
		t.Errorf("a refused say was sent %d times", len(broker.got))
	}
	broker.status = 0
	rou2Of(r).closeFails(errors.New("rou2 is away"))
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err == nil {
		t.Fatal("the release was to fail")
	}
	if err := r.hs.SayAs(context.Background(), lease.Box, lease.Aria, "t"); err == nil {
		t.Error("a lease that is being released said")
	}
	rou2Of(r).closeFails(nil)
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Fatal(err)
	}
	if err := r.hs.SayAs(context.Background(), lease.Box, lease.Aria, "t"); err == nil {
		t.Error("a released lease said")
	}
}

// The lease's principal holds say on its own lease and nothing else,
// whatever the policy file binds: every other method is refused it.
func TestALeasesPrincipalHoldsSayAndNothingElse(t *testing.T) {
	r, _, lease := sayRigUnder(t, leasingPolicy+`
[[bind]]
role = "owner"
principals = ["sandbox:*"]
`)
	ctx := angelus.AsLeasedBox(lease.Box)
	if err := r.hs.SayAs(context.Background(), lease.Box, lease.Aria, "t"); err != nil {
		t.Fatalf("the lease's own say was refused: %v", err)
	}
	for method, params := range map[string]any{
		rpc.MethodList:        map[string]any{},
		rpc.MethodKill:        rpc.KillRequest{FigaroID: lease.Aria},
		rpc.MethodSlotLease:   rpc.SlotLeaseRequest{Pool: "groups", Key: "chat:12"},
		rpc.MethodSlotDeliver: rpc.SlotDeliverRequest{Pool: "groups", Key: "chat:10", Generation: 1, ID: "m", Text: "t"},
		rpc.MethodFormCreate:  map[string]any{},
	} {
		claimed, _ := rpc.WithCaller(params, lease.Aria, nil)
		bare, _ := json.Marshal(params)
		for how, raw := range map[string][]byte{"claiming its figaro": claimed, "claiming none": bare} {
			if _, err := r.h[method](ctx, raw); err == nil {
				t.Errorf("the lease's principal %s was served %s", how, method)
			}
		}
	}
}

// A lease says at most say_per_minute times in any minute. A say refused
// for it reaches no broker; a minute after the first, the lease says
// again. The count is the lease's own: another lease is not slowed.
func TestALeaseSaysAtMostItsBoundInAnyMinute(t *testing.T) {
	r, broker, lease := sayRig(t)
	other, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:11"}`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 21, 0, 0, 0, time.UTC)
	r.a.Slots.Now = func() time.Time { return now }
	ctx := context.Background()
	for i := range config.DefaultSayPerMinute {
		if err := r.hs.SayAs(ctx, lease.Box, lease.Aria, "t"); err != nil {
			t.Fatalf("say %d: %v", i+1, err)
		}
		now = now.Add(time.Second)
	}
	if err := r.hs.SayAs(ctx, lease.Box, lease.Aria, "t"); err == nil || !strings.Contains(err.Error(), "say_per_minute") {
		t.Errorf("a say over the bound answered %v", err)
	}
	if len(broker.got) != config.DefaultSayPerMinute {
		t.Errorf("broker was asked %d times, want %d", len(broker.got), config.DefaultSayPerMinute)
	}
	if err := r.hs.SayAs(ctx, other.Box, other.Aria, "t"); err != nil {
		t.Errorf("another lease was slowed: %v", err)
	}
	now = now.Add(time.Minute - time.Duration(config.DefaultSayPerMinute)*time.Second)
	if err := r.hs.SayAs(ctx, lease.Box, lease.Aria, "t"); err != nil {
		t.Errorf("a say a minute after the first: %v", err)
	}
	if err := r.hs.SayAs(ctx, lease.Box, lease.Aria, "t"); err == nil {
		t.Error("the second say in that second was posted")
	}
}
