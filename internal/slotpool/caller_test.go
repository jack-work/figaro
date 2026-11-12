package slotpool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeBearer struct {
	mu          sync.Mutex
	tokens      []string
	invalidated []string
}

func (b *fakeBearer) Resolve() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens[0], nil
}

func (b *fakeBearer) Invalidate(token string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.invalidated = append(b.invalidated, token)
	if len(b.tokens) < 2 {
		return errors.New("no better token")
	}
	b.tokens = b.tokens[1:]
	return nil
}

type rou2Call struct {
	method, path, auth, body string
}

// A rou2 as El Aduanero's B4 names it: a lease opens once per id, an open id
// answers again for the same class, a closed id never reopens, and a close
// of an id nobody opened says so by type.
type fakeRou2 struct {
	mu     sync.Mutex
	calls  []rou2Call
	open   map[string]string
	closed map[string]bool
	token  string
	broken int
}

func (f *fakeRou2) refuse(w http.ResponseWriter, status int, kind string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": kind, "message": "said by rou2"}})
}

func (f *fakeRou2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, rou2Call{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(body)})
	if f.broken != 0 {
		w.WriteHeader(f.broken)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.refuse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, named := strings.CutPrefix(r.URL.Path, "/v1/leases/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
		var req struct{ ID, Class string }
		_ = json.Unmarshal(body, &req)
		switch class, open := f.open[req.ID]; {
		case f.closed[req.ID]:
			f.refuse(w, http.StatusConflict, "lease_closed")
		case open && class != req.Class:
			f.refuse(w, http.StatusConflict, "lease_class")
		default:
			f.open[req.ID] = req.Class
			_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "class": req.Class, "caps": map[string]int{"usd_total": 5}})
		}
	case r.Method == http.MethodDelete && named:
		switch _, open := f.open[id]; {
		case f.closed[id]:
			f.refuse(w, http.StatusForbidden, "lease_closed")
		case !open:
			f.refuse(w, http.StatusForbidden, "lease_unknown")
		default:
			delete(f.open, id)
			f.closed[id] = true
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.NotFound(w, r)
	}
}

func rou2Rig(t *testing.T) (*fakeRou2, *fakeBearer, Rou2) {
	t.Helper()
	fake := &fakeRou2{open: map[string]string{}, closed: map[string]bool{}, token: "good"}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	bearer := &fakeBearer{tokens: []string{"good"}}
	return fake, bearer, Rou2{Base: srv.URL + "/v1/", Bearer: bearer, HTTP: srv.Client()}
}

func TestARou2LeaseOpensOnceClosesForGoodAndNeverReopens(t *testing.T) {
	fake, _, rou2 := rou2Rig(t)
	ctx := context.Background()
	id := CallerID("chat-groups", "slot3-g7")
	if id != "chat-groups/slot3-g7" {
		t.Fatalf("the lease id is %q", id)
	}
	if err := rou2.Open(ctx, id, "chat-group"); err != nil {
		t.Fatal(err)
	}
	if first := fake.calls[0]; first.method != "POST" || first.path != "/v1/leases" || first.auth != "Bearer good" || first.body != `{"class":"chat-group","id":"chat-groups/slot3-g7"}` {
		t.Errorf("the open was sent as %+v", first)
	}
	if err := rou2.Open(ctx, id, "chat-group"); err != nil {
		t.Errorf("an open of an open lease with the same class: %v", err)
	}
	if err := rou2.Open(ctx, id, "another"); err == nil {
		t.Error("an open lease was opened again under another class")
	}
	if err := rou2.Close(ctx, id); err != nil {
		t.Fatal(err)
	}
	if last := fake.calls[len(fake.calls)-1]; last.method != "DELETE" || last.path != "/v1/leases/chat-groups/slot3-g7" {
		t.Errorf("the close was sent as %+v", last)
	}
	if len(fake.open) != 0 || !fake.closed[id] {
		t.Errorf("after the close rou2 holds open %v, closed %v", fake.open, fake.closed)
	}
	if err := rou2.Close(ctx, id); err != nil {
		t.Errorf("a close of a closed lease is done, and it answered %v", err)
	}
	if err := rou2.Close(ctx, CallerID("chat-groups", "slot3-g8")); err != nil {
		t.Errorf("a close of a lease rou2 never opened is done, and it answered %v", err)
	}
	if err := rou2.Open(ctx, id, "chat-group"); !errors.Is(err, ErrCallerClosed) {
		t.Errorf("an open of a closed lease answered %v", err)
	}
}

// Every answer the client does not know is a failure: a release that took a
// routing 404 or a 5xx for a close would free a slot whose lease still
// spends.
func TestARou2AnswerThatIsNoLeaseAnswerIsAFailure(t *testing.T) {
	fake, _, rou2 := rou2Rig(t)
	ctx := context.Background()
	id := CallerID("groups", "slot0-g1")
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusBadGateway, http.StatusForbidden} {
		fake.broken = status
		if err := rou2.Open(ctx, id, "group"); err == nil {
			t.Errorf("an open answered %d passed", status)
		}
		if err := rou2.Close(ctx, id); err == nil {
			t.Errorf("a close answered %d passed", status)
		}
	}
	fake.broken = 0
	wrong := Rou2{Base: rou2.Base, Bearer: rou2.Bearer, HTTP: &http.Client{Transport: answering(`{"id":"groups/slot0-g2","class":"group"}`)}}
	if err := wrong.Open(ctx, id, "group"); err == nil {
		t.Error("an open answered for another lease passed")
	}
	if err := (Rou2{Base: "http://127.0.0.1:1/v1", Bearer: rou2.Bearer}).Close(ctx, id); err == nil {
		t.Error("a close that reached nothing passed")
	}
}

type answering string

func (a answering) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(a))), Header: http.Header{}}, nil
}

func TestARefusedRou2TokenIsInvalidatedAndTheCallMadeOnceMore(t *testing.T) {
	fake, bearer, rou2 := rou2Rig(t)
	bearer.tokens = []string{"stale", "good"}
	if err := rou2.Open(context.Background(), "groups/slot0-g1", "group"); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 || len(bearer.invalidated) != 1 || bearer.invalidated[0] != "stale" {
		t.Errorf("%d calls, invalidated %v", len(fake.calls), bearer.invalidated)
	}
	bearer.tokens = []string{"stale"}
	if err := rou2.Open(context.Background(), "groups/slot0-g2", "group"); err == nil {
		t.Error("an open with a refused token and no better one passed")
	}
}
