package routed

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apimodels "github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/catalogmaps"
)

type etagGateway struct {
	mu      sync.Mutex
	body    string
	etag    string
	paged   bool
	asked   []string
	answers []int
}

func (g *etagGateway) set(body, etag string) {
	g.mu.Lock()
	g.body, g.etag = body, etag
	g.mu.Unlock()
}

func (g *etagGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	inm := r.Header.Get("If-None-Match")
	g.asked = append(g.asked, inm)
	if g.paged && r.URL.Query().Get("after_id") == "" {
		w.Header().Set("ETag", `"page-one"`)
		g.answers = append(g.answers, 200)
		io.WriteString(w, `{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`)
		return
	}
	w.Header().Set("ETag", g.etag)
	if inm != "" && inm == g.etag {
		g.answers = append(g.answers, 304)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	g.answers = append(g.answers, 200)
	io.WriteString(w, g.body)
}

func (g *etagGateway) log() ([]string, []int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.asked...), append([]int(nil), g.answers...)
}

func etagProvider(t *testing.T, g *etagGateway) (*Provider, *testClock) {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	mapping := catalogmaps.Builtin("generic")
	if g.paged {
		mapping.More, mapping.Cursor, mapping.CursorParam = "has_more", "last_id", "after_id"
	}
	p := mustNew(t, Spec{Name: "etag-" + t.Name(), BaseURL: func() string { return srv.URL + "/v1" }, Catalog: "/models", Default: Chat, Mapping: mapping})
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	p.now = clock.now
	return p, clock
}

func listedIDs(t *testing.T, p *Provider) string {
	t.Helper()
	listed, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range listed {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

// rou2 v0.7.0 answers a refresh whose ETag still matches with 304 and no
// body. The provider must send the tag it holds, keep its catalog on the
// 304, count the refresh as fresh, and take a changed catalog whole.
func TestACatalogRefreshRevalidatesWithTheHeldETag(t *testing.T) {
	g := &etagGateway{body: `{"data":[{"id":"a"}]}`, etag: `"v1"`}
	p, clock := etagProvider(t, g)
	if got := listedIDs(t, p); got != "a" {
		t.Fatalf("first listing %q", got)
	}
	clock.advance(catalogTTL + time.Minute)
	if !p.cat.stale(p.now(), catalogTTL) {
		t.Fatal("the catalog should be stale before the refresh")
	}
	if got := listedIDs(t, p); got != "a" {
		t.Fatalf("a 304 lost the catalog: %q", got)
	}
	if p.cat.stale(p.now(), catalogTTL) {
		t.Fatal("a 304 did not count as a refresh")
	}
	if _, known := p.cat.lookup("a"); !known {
		t.Fatal("a 304 emptied the route table")
	}
	g.set(`{"data":[{"id":"a"},{"id":"b"}]}`, `"v2"`)
	if got := listedIDs(t, p); got != "a,b" {
		t.Fatalf("a changed catalog was not taken: %q", got)
	}
	if got := listedIDs(t, p); got != "a,b" {
		t.Fatalf("after revalidating v2: %q", got)
	}
	asked, answers := g.log()
	wantAsked := []string{"", `"v1"`, `"v1"`, `"v2"`}
	wantAnswers := []int{200, 304, 200, 304}
	if strings.Join(asked, " ") != strings.Join(wantAsked, " ") || len(answers) != len(wantAnswers) {
		t.Fatalf("If-None-Match sent %q answered %v, want %q answered %v", asked, answers, wantAsked, wantAnswers)
	}
	for i := range answers {
		if answers[i] != wantAnswers[i] {
			t.Fatalf("answers %v, want %v", answers, wantAnswers)
		}
	}
}

// One page's tag says nothing about the pages behind it, so a paged
// catalog is always fetched whole.
func TestAPagedCatalogIsNeverRevalidated(t *testing.T) {
	g := &etagGateway{body: `{"data":[{"id":"second"}],"has_more":false}`, etag: `"tail"`, paged: true}
	p, _ := etagProvider(t, g)
	for range 2 {
		if got := listedIDs(t, p); got != "first,second" {
			t.Fatalf("listing %q", got)
		}
	}
	asked, _ := g.log()
	for _, inm := range asked {
		if inm != "" {
			t.Fatalf("a paged catalog sent If-None-Match: %q", asked)
		}
	}
}

// A catalog seeded from the persisted IR holds no listing to keep, so its
// first refresh must not ask for a 304.
func TestASeededCatalogRefreshesUnconditionally(t *testing.T) {
	g := &etagGateway{body: `{"data":[{"id":"a"}]}`, etag: `"v1"`}
	p, _ := etagProvider(t, g)
	p.cat.seed(map[string]apimodels.Entry{"a": {}}, p.now())
	if got := listedIDs(t, p); got != "a" {
		t.Fatalf("listing %q", got)
	}
	if asked, _ := g.log(); len(asked) != 1 || asked[0] != "" {
		t.Fatalf("seeded refresh sent If-None-Match %q", asked)
	}
}
