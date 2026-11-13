package angelus_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

const searchKey = "canary-search-key-7f3a"

type searchEndpoint struct {
	mu       sync.Mutex
	status   int
	answer   string
	location string
	got      []*http.Request
	bodies   []string
}

func (e *searchEndpoint) RoundTrip(r *http.Request) (*http.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	e.got = append(e.got, r)
	e.bodies = append(e.bodies, string(body))
	status, answer := e.status, e.answer
	if status == 0 {
		status = http.StatusOK
	}
	if answer == "" {
		answer = `{"results":[{"title":"One","url":"https://one.example/","snippet":"first"},{"title":"Two","url":"https://two.example/","snippet":"second"},{"title":"Three","url":"https://three.example/","snippet":"third"}]}`
	}
	header := http.Header{}
	if e.location != "" {
		header.Set("Location", e.location)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(answer)), Header: header, Request: r}, nil
}

func searchRig(t *testing.T) (grantRig, *searchEndpoint, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "search-groups"), []byte(searchKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	r, _, lease := sayRig(t)
	end := &searchEndpoint{}
	r.a.Workers.SearchWire = end
	return r, end, lease.Box, lease.Aria
}

// The tool posts the query and the count, as figaro's own JSON, to the
// pool's search endpoint with the pool's credential as a bearer, and gives
// the model the results it asked for as text.
func TestALeasedFigaroSearchesThroughItsPoolsEndpoint(t *testing.T) {
	r, end, box, aria := searchRig(t)
	out, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", map[string]any{"query": "figaro opera", "count": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if len(end.got) != 1 {
		t.Fatalf("the endpoint was asked %d times", len(end.got))
	}
	req := end.got[0]
	if req.Method != http.MethodPost || req.URL.String() != "http://127.0.0.1:4390/search" || req.Header.Get("Authorization") != "Bearer "+searchKey {
		t.Errorf("the search went as %s %s with %q", req.Method, req.URL, req.Header.Get("Authorization"))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(end.bodies[0]), &body); err != nil || len(body) != 2 || body["query"] != "figaro opera" || body["count"] != float64(2) {
		t.Errorf("the endpoint was sent %s", end.bodies[0])
	}
	if !strings.Contains(out, "One\nhttps://one.example/\nfirst") || !strings.Contains(out, "Two") || strings.Contains(out, "Three") {
		t.Errorf("the model was given %q, want the first two results", out)
	}
	if out, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", map[string]any{"query": "figaro"}); err != nil || !strings.Contains(out, "Three") {
		t.Errorf("a search with no count answered %q (%v), want every result up to five", out, err)
	}
	end.answer = `{"results":[]}`
	if out, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", map[string]any{"query": "nothing"}); err != nil || out != "no results" {
		t.Errorf("an empty answer gave %q (%v)", out, err)
	}
}

// What a search is refused for, before any request leaves: a figaro that
// does not run in the lease's box, no figaro, a box no lease holds, a
// released lease, and arguments out of shape.
func TestASearchIsRefusedForAnyoneButTheLeasesOwnFigaro(t *testing.T) {
	r, end, box, aria := searchRig(t)
	other, err := r.lease(asBroker(), `{"pool":"groups","key":"chat:11"}`)
	if err != nil {
		t.Fatal(err)
	}
	ask := map[string]any{"query": "q"}
	for why, figaro := range map[string]string{"another lease's figaro": other.Aria, "no figaro": ""} {
		if _, err := r.hs.RunToolAtWake(context.Background(), box, figaro, "search", ask); err == nil {
			t.Errorf("a search claiming %s was made", why)
		}
	}
	if _, err := r.hs.RunToolAtWake(context.Background(), "slot1-g7", aria, "search", ask); err == nil {
		t.Error("a search from a box no lease holds was made")
	}
	for why, args := range map[string]map[string]any{
		"no query":             {},
		"a blank query":        {"query": "  "},
		"a query too long":     {"query": strings.Repeat("q", 401)},
		"eleven results":       {"query": "q", "count": float64(11)},
		"no results":           {"query": "q", "count": float64(0)},
		"a fraction of one":    {"query": "q", "count": 2.5},
		"a count that is text": {"query": "q", "count": "3"},
	} {
		if _, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", args); err == nil {
			t.Errorf("a search with %s was made", why)
		}
	}
	if err := r.release(asBroker(), `{"pool":"groups","key":"chat:10","generation":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", ask); err == nil {
		t.Error("a released lease searched")
	}
	if len(end.got) != 0 {
		t.Errorf("refused searches reached the endpoint %d times", len(end.got))
	}
}

// The credential and the endpoint's body never reach the model or the
// figaro's form: a refusal is named by its status alone, a redirect is not
// followed, an answer that is not a list of results is an error, and an
// answer over the cap is not read whole.
func TestTheSearchCredentialAndTheEndpointsBodyStayInTheDaemon(t *testing.T) {
	r, end, box, aria := searchRig(t)
	leak := "said by the endpoint " + searchKey
	for _, c := range []struct {
		status          int
		answer, wantErr string
		location        string
	}{
		{http.StatusUnauthorized, leak, "401", ""},
		{http.StatusInternalServerError, leak, "500", ""},
		{http.StatusFound, leak, "302", "https://elsewhere.example/"},
		{http.StatusOK, leak, "not a list of results", ""},
		{http.StatusOK, `{"error":"` + leak + `"}`, "not a list of results", ""},
		{http.StatusOK, `{"results":[{"title":"` + strings.Repeat("x", 257<<10) + `"}]}`, "over 256 KiB", ""},
	} {
		end.status, end.answer, end.location = c.status, c.answer, c.location
		before := len(end.got)
		out, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", map[string]any{"query": "q"})
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("an answer %d %.40q gave %v, want %q", c.status, c.answer, err, c.wantErr)
		}
		if err != nil && (strings.Contains(err.Error(), searchKey) || strings.Contains(err.Error(), "said by")) || strings.Contains(out, searchKey) {
			t.Errorf("an answer %d reached the model: %q %v", c.status, out, err)
		}
		if len(end.got) != before+1 {
			t.Errorf("an answer %d was asked %d times", c.status, len(end.got)-before)
		}
	}
	snap, err := r.backend.FormState(aria)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(snap); strings.Contains(string(raw), searchKey) {
		t.Error("the figaro's form holds the search credential")
	}
	if err := os.Remove(filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), "search-groups")); err != nil {
		t.Fatal(err)
	}
	end.status, end.answer = 0, ""
	before := len(end.got)
	if _, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", map[string]any{"query": "q"}); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Errorf("a search with its credential gone answered %v", err)
	}
	if len(end.got) != before {
		t.Error("a search with no credential was sent without one")
	}
}

// A lease searches at most search_per_minute times in any minute and
// search_per_day in any day. A search refused for it reaches no endpoint.
func TestALeaseSearchesAtMostItsBounds(t *testing.T) {
	r, end, box, aria := searchRig(t)
	now := time.Date(2026, 11, 12, 21, 0, 0, 0, time.UTC)
	r.a.Workers.Now = func() time.Time { return now }
	search := func() error {
		_, err := r.hs.RunToolAtWake(context.Background(), box, aria, "search", map[string]any{"query": "q"})
		return err
	}
	for i := range config.DefaultSearchPerMinute {
		if err := search(); err != nil {
			t.Fatalf("search %d: %v", i+1, err)
		}
	}
	if err := search(); err == nil || !strings.Contains(err.Error(), "search_per_minute") {
		t.Errorf("a search over the minute's bound answered %v", err)
	}
	for len(end.got) < config.DefaultSearchPerDay {
		now = now.Add(time.Minute)
		for range config.DefaultSearchPerMinute {
			if len(end.got) == config.DefaultSearchPerDay {
				break
			}
			if err := search(); err != nil {
				t.Fatalf("search %d: %v", len(end.got)+1, err)
			}
		}
	}
	now = now.Add(time.Minute)
	if err := search(); err == nil || !strings.Contains(err.Error(), "search_per_day") {
		t.Errorf("a search over the day's bound answered %v", err)
	}
	if len(end.got) != config.DefaultSearchPerDay {
		t.Errorf("the endpoint was asked %d times, want %d", len(end.got), config.DefaultSearchPerDay)
	}
	now = time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)
	if err := search(); err != nil {
		t.Errorf("a search a day after the first: %v", err)
	}
}
