package sandbox

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	api "github.com/jack-work/figaro/api/sandbox"
)

type httpProvider struct{ url string }

func (p httpProvider) Open(ctx context.Context, spec api.Spec) (api.Sandbox, error) {
	b := &httpBox{url: p.url, client: &http.Client{Transport: api.FigaroTransport(spec.Figaro, nil)}}
	if _, err := b.do(ctx, http.MethodPost, "/open", nil); err != nil {
		return nil, err
	}
	return b, nil
}

type httpBox struct {
	api.Sandbox
	url    string
	client *http.Client
}

type httpFS struct {
	api.FS
	b *httpBox
}

func (b *httpBox) do(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, b.url+path, body)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b *httpBox) FS() api.FS { return httpFS{b: b} }

func (f httpFS) ReadFile(path string) ([]byte, error) {
	return f.b.do(context.Background(), http.MethodGet, "/fs/"+path, nil)
}

func (f httpFS) WriteFile(path string, data []byte, _ fs.FileMode) error {
	_, err := f.b.do(context.Background(), http.MethodPut, "/fs/"+path, strings.NewReader(string(data)))
	return err
}

func (b *httpBox) Close() error {
	_, err := b.do(context.Background(), http.MethodDelete, "/open", nil)
	return err
}

type seen struct {
	mu   sync.Mutex
	reqs []string
}

func (s *seen) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, fmt.Sprintf("%s %s %q", r.Method, r.URL.Path, r.Header.Values(api.FigaroHeader)))
	s.mu.Unlock()
}

// An HTTP provider is one value serving many figaros; each request names
// the figaro of the Open it belongs to, whatever the caller tried to put
// there.
func TestAnHTTPProviderSeesTheFigaroOnEveryRequest(t *testing.T) {
	s := &seen{}
	srv := httptest.NewServer(s)
	defer srv.Close()
	prov := httpProvider{url: srv.URL}
	a, err := prov.Open(context.Background(), api.Spec{Figaro: "aaaa"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := prov.Open(context.Background(), api.Spec{Figaro: "bbbb"})
	if err != nil {
		t.Fatal(err)
	}
	a.FS().WriteFile("x", []byte("1"), 0o644)
	b.FS().ReadFile("x")
	forged, _ := http.NewRequest(http.MethodGet, srv.URL+"/forged", nil)
	forged.Header.Set(api.FigaroHeader, "bbbb")
	if _, err := a.(*httpBox).client.Do(forged); err != nil {
		t.Fatal(err)
	}
	if got := forged.Header.Get(api.FigaroHeader); got != "bbbb" {
		t.Fatalf("the transport changed the caller's request: %q", got)
	}
	a.Close()
	b.Close()
	want := []string{
		`POST /open ["aaaa"]`,
		`POST /open ["bbbb"]`,
		`PUT /fs/x ["aaaa"]`,
		`GET /fs/x ["bbbb"]`,
		`GET /forged ["aaaa"]`,
		`DELETE /open ["aaaa"]`,
		`DELETE /open ["bbbb"]`,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Join(s.reqs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("server saw\n%s\nwant\n%s", strings.Join(s.reqs, "\n"), strings.Join(want, "\n"))
	}
}
