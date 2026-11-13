package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

type reloadRig struct {
	t     *testing.T
	dir   string
	a     *angelus.Angelus
	extra string
}

type rigBox struct {
	name, endpoint, host string
}

func (r *reloadRig) write(boxes ...rigBox) {
	r.t.Helper()
	var b strings.Builder
	b.WriteString(r.extra)
	for _, x := range boxes {
		fmt.Fprintf(&b, "[sandbox.boxes.%s]\nexec = %q\nendpoint = %q\noutfits = [\"*\"]\nproviders = [\"*\"]\n", x.name, filepath.Join(r.dir, x.name+"-exec.sock"), x.endpoint)
		if x.host != "" {
			fmt.Fprintf(&b, "host_endpoint = %q\n", x.host)
		}
	}
	if err := os.WriteFile(filepath.Join(r.dir, "config.toml"), []byte(b.String()), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *reloadRig) path(parts ...string) string {
	p := filepath.Join(append([]string{r.dir}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func newReloadRig(t *testing.T, boxes ...rigBox) *reloadRig {
	t.Helper()
	r := &reloadRig{t: t, dir: t.TempDir()}
	r.write(boxes...)
	loaded, err := config.Load(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	backend := store.NewTestBackend(t)
	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: backend})
	a.SocketPath = filepath.Join(r.dir, "angelus.sock")
	a.Settings = loaded
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}, angelus.StoreFacts(backend))
	a.Policies.SetBoxes(loaded.BoxesWithEndpoints())
	if err := a.Policies.Reload(""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{Angelus: a, Config: loaded, Ctx: ctx}).Map
	if err := a.OpenBoxEndpoints(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	r.a = a
	return r
}

func (r *reloadRig) refresh() error {
	ctx := authz.WithPrincipal(authz.WithEndpoint(context.Background(), authz.EndpointLocal), rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"})
	raw, _ := json.Marshal(rpc.ConfigureRequest{Refresh: true})
	_, err := r.a.Handlers[rpc.MethodConfigure](ctx, raw)
	return err
}

type boxCaller struct {
	c      *jkrpc.Client
	lapsed chan string
}

func dialBox(path string) (*boxCaller, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	b := &boxCaller{lapsed: make(chan string, 4)}
	b.c = jkrpc.NewClient(jkrpc.NewConn(nc), func(method string, params json.RawMessage) {
		if method != rpc.MethodEndpointLapsed {
			return
		}
		var l rpc.EndpointLapsed
		_ = json.Unmarshal(params, &l)
		b.lapsed <- l.Reason
	})
	return b, nil
}

func (b *boxCaller) list() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out rpc.ListResponse
	return b.c.Call(ctx, rpc.MethodList, rpc.ListRequest{}, &out)
}

func (b *boxCaller) lapseReason(t *testing.T) string {
	t.Helper()
	select {
	case r := <-b.lapsed:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the held connection got no endpoint.lapsed")
		return ""
	}
}

func reaches(t *testing.T, path string) error {
	t.Helper()
	b, err := dialBox(path)
	if err != nil {
		return err
	}
	defer b.c.Close()
	return b.list()
}

func TestABoxAddedAtRuntimeOpensItsEndpointOnRefresh(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	if reaches(t, endpoint) == nil {
		t.Fatal("canary: the endpoint answers before the box exists")
	}
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("a command in the added box does not reach the daemon: %v", err)
	}
}

func TestABoxRemovedAtRuntimeShutsItsEndpointWithAReason(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	held, err := dialBox(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer held.c.Close()
	if err := held.list(); err != nil {
		t.Fatalf("canary: the held connection never worked: %v", err)
	}
	r.write()
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := held.lapseReason(t); !strings.Contains(got, "box chat's endpoint left the config") {
		t.Fatalf("lapse reason %q", got)
	}
	if err := held.list(); err == nil {
		t.Fatal("the held connection still serves after its box left")
	}
	if err := reaches(t, endpoint); err == nil {
		t.Fatal("a new call through the removed box's endpoint still reaches the daemon")
	}
	if _, err := os.Stat(endpoint); !os.IsNotExist(err) {
		t.Fatalf("the removed endpoint's socket stays: %v", err)
	}
}

func TestABoxWrittenWithoutARefreshHasNoEndpoint(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := reaches(t, endpoint); err == nil {
		t.Fatal("an endpoint opened with no refresh")
	}
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("canary: the written config never opens an endpoint: %v", err)
	}
}

func TestABoxEndpointMovedAtRuntimeReopensAtItsNewPath(t *testing.T) {
	r := newReloadRig(t)
	old := r.path("chat", "ep", "ep.sock")
	moved := r.path("chat", "ep2", "ep.sock")
	r.write(rigBox{name: "chat", endpoint: old})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	held, err := dialBox(old)
	if err != nil {
		t.Fatal(err)
	}
	defer held.c.Close()
	if err := held.list(); err != nil {
		t.Fatalf("canary: the old endpoint never worked: %v", err)
	}
	r.write(rigBox{name: "chat", endpoint: moved})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := held.lapseReason(t); !strings.Contains(got, "moved to "+moved) {
		t.Fatalf("lapse reason %q", got)
	}
	if err := reaches(t, moved); err != nil {
		t.Fatalf("the moved endpoint does not serve: %v", err)
	}
	if err := reaches(t, old); err == nil {
		t.Fatal("the old path still serves after the move")
	}
}

func TestABoxRenamedOnTheSamePathServesTheNewBox(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("slot", "ep", "ep.sock")
	r.write(rigBox{name: "slot1", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	held, err := dialBox(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer held.c.Close()
	if err := held.list(); err != nil {
		t.Fatalf("canary: slot1's endpoint never worked: %v", err)
	}
	r.write(rigBox{name: "slot2", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if got := held.lapseReason(t); !strings.Contains(got, "box slot1's endpoint left the config") {
		t.Fatalf("a connection admitted as slot1 lapsed with %q", got)
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("slot2 does not answer on the path slot1 gave up: %v", err)
	}
}

func TestAReloadThatBreaksAHostEndpointRuleKeepsTheOldEndpoints(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("canary: the first endpoint never worked: %v", err)
	}
	other := r.path("other", "ep", "ep.sock")
	inside := filepath.Join(filepath.Dir(endpoint), "host.sock")
	r.write(rigBox{name: "chat", endpoint: endpoint}, rigBox{name: "other", endpoint: other, host: inside})
	err := r.refresh()
	if err == nil || !strings.Contains(err.Error(), "a host endpoint lives where no box does") {
		t.Fatalf("a host endpoint inside a box's endpoint directory was accepted on reload: %v", err)
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("the old endpoint stopped serving after a refused reload: %v", err)
	}
	for _, p := range []string{other, inside} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("a refused reload left %s behind: %v", p, err)
		}
	}
}

func TestAReloadWhoseEndpointCannotOpenNeverHalfApplies(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	added := r.path("added", "ep", "ep.sock")
	missing := filepath.Join(r.dir, "nowhere", "ep.sock")
	r.write(rigBox{name: "added", endpoint: added}, rigBox{name: "chat", endpoint: endpoint}, rigBox{name: "zbroken", endpoint: missing})
	err := r.refresh()
	if err == nil || !strings.Contains(err.Error(), "sandbox.boxes.zbroken.endpoint") {
		t.Fatalf("an endpoint that cannot listen was accepted: %v", err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Fatalf("a refused reload half-applied: the added box's endpoint opened: %v", err)
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("the old endpoint stopped serving after a refused reload: %v", err)
	}
	r.write(rigBox{name: "added", endpoint: added}, rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, added); err != nil {
		t.Fatalf("canary: the added box never opens without the broken one: %v", err)
	}
}

func TestAHostEndpointAddedAtRuntimeOpens(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	host := r.path("chat", "host", "host.sock")
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if reaches(t, host) == nil {
		t.Fatal("canary: the host endpoint answers before it is configured")
	}
	r.write(rigBox{name: "chat", endpoint: endpoint, host: host})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, host); err != nil {
		t.Fatalf("a host endpoint added at runtime does not serve: %v", err)
	}
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := reaches(t, host); err == nil {
		t.Fatal("a host endpoint removed at runtime still serves")
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("dropping the host endpoint took the box endpoint with it: %v", err)
	}
}

func TestAEndpointMovedToAPathThatCannotListenKeepsTheOldEndpointAndItsConnections(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	r.write(rigBox{name: "chat", endpoint: endpoint})
	if err := r.refresh(); err != nil {
		t.Fatal(err)
	}
	held, err := dialBox(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer held.c.Close()
	if err := held.list(); err != nil {
		t.Fatalf("canary: the old endpoint never served: %v", err)
	}
	r.write(rigBox{name: "chat", endpoint: filepath.Join(r.dir, "nowhere", "ep.sock")})
	if err := r.refresh(); err == nil || !strings.Contains(err.Error(), "sandbox.boxes.chat.endpoint") {
		t.Fatalf("a move to a path that cannot listen was accepted: %v", err)
	}
	select {
	case reason := <-held.lapsed:
		t.Fatalf("a refused move lapsed the old endpoint's connection: %q", reason)
	default:
	}
	if err := held.list(); err != nil {
		t.Fatalf("the held connection stopped serving after a refused move: %v", err)
	}
	if err := reaches(t, endpoint); err != nil {
		t.Fatalf("the old endpoint stopped answering after a refused move: %v", err)
	}
}

func TestABoxEndpointOnTheCLIOrWebEndpointIsRefusedByName(t *testing.T) {
	web := filepath.Join(t.TempDir(), "web.sock")
	for _, tc := range []struct{ name, path, want string }{
		{"cli", "", "is the CLI endpoint"},
		{"web", web, "is the web endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReloadRig(t)
			endpoint := r.path("chat", "ep", "ep.sock")
			r.extra = fmt.Sprintf("[endpoint.web]\npath = %q\n", web)
			r.write(rigBox{name: "chat", endpoint: endpoint})
			if err := r.refresh(); err != nil {
				t.Fatal(err)
			}
			if err := reaches(t, endpoint); err != nil {
				t.Fatalf("canary: the first endpoint never served: %v", err)
			}
			clash := tc.path
			if clash == "" {
				clash = r.a.SocketPath
			}
			r.write(rigBox{name: "chat", endpoint: endpoint}, rigBox{name: "intruder", endpoint: clash})
			err := r.refresh()
			if err == nil || !strings.Contains(err.Error(), "sandbox.boxes.intruder.endpoint: "+clash+" "+tc.want) {
				t.Fatalf("a box endpoint on the %s endpoint was not refused by name: %v", tc.name, err)
			}
			if err := reaches(t, endpoint); err != nil {
				t.Fatalf("the old endpoint stopped serving after the refusal: %v", err)
			}
			if _, err := os.Stat(clash); !os.IsNotExist(err) {
				t.Fatalf("the refused box listened on %s: %v", clash, err)
			}
		})
	}
}

func TestARefreshWhosePolicyFailsSaysTheEndpointsAlreadyChanged(t *testing.T) {
	r := newReloadRig(t)
	endpoint := r.path("chat", "ep", "ep.sock")
	r.extra = fmt.Sprintf("[authz]\npolicy = %q\n", filepath.Join(r.dir, "missing-policy.toml"))
	r.write(rigBox{name: "chat", endpoint: endpoint})
	err := r.refresh()
	if err == nil || !strings.Contains(err.Error(), "the new config and its box endpoints are in force") {
		t.Fatalf("a policy failure after the endpoints applied did not say so: %v", err)
	}
	if _, serr := os.Stat(endpoint); serr != nil {
		t.Fatalf("canary: the endpoints were not in force when the policy failed: %v", serr)
	}
}
