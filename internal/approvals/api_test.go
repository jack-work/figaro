package approvals

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

func apiDo(h http.Handler, method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.1:4000"
	r.Header.Set(EdgeHeader, edgeSecret)
	r.Header.Set("Remote-User", "gluck")
	r.Header.Set("Origin", "https://order.kelliher.info")
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
			continue
		}
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAPIListsTheExactPatchAndApprovesOnce(t *testing.T) {
	f, h := serve(t)
	w := apiDo(h, "GET", "/api/v1/requests", nil, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("list: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	var list struct {
		Requests []apiRequest `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Requests) != 1 {
		t.Fatalf("list body: %v %s", err, w.Body.String())
	}
	req := list.Requests[0]
	if req.Hash == "" || req.Token == "" || len(req.Lines) < 2 || req.Lines[len(req.Lines)-1].Op != "remove" {
		t.Fatalf("patch not shown whole: %+v", req)
	}
	path := "/api/v1/requests/" + url.PathEscape(req.Filing) + "/" + req.ID
	if w := apiDo(h, "GET", path, nil, ""); w.Code != 200 {
		t.Fatalf("one: %d", w.Code)
	}
	body, _ := json.Marshal(apiDecision{Hash: req.Hash, Version: req.Version, Token: req.Token})
	json := map[string]string{"Content-Type": "application/json"}
	if w := apiDo(h, "POST", path+"/approve", json, string(body)); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if get(t, f, seat, "system.model") != `"claude-opus-4-5"` {
		t.Fatal("not applied")
	}
	if w := apiDo(h, "POST", path+"/approve", json, string(body)); w.Code != http.StatusForbidden && w.Code != http.StatusConflict {
		t.Fatalf("second click: %d", w.Code)
	}
}

func TestAPIPostsKeepEveryGate(t *testing.T) {
	f, h := serve(t)
	req := func() apiRequest {
		var l struct {
			Requests []apiRequest `json:"requests"`
		}
		_ = json.Unmarshal(apiDo(h, "GET", "/api/v1/requests", nil, "").Body.Bytes(), &l)
		return l.Requests[0]
	}()
	path := "/api/v1/requests/" + url.PathEscape(req.Filing) + "/" + req.ID + "/approve"
	body, _ := json.Marshal(apiDecision{Hash: req.Hash, Version: req.Version, Token: req.Token})
	j := "application/json"
	cases := []struct {
		name    string
		headers map[string]string
		body    string
		want    int
	}{
		{"form content type", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, string(body), 415},
		{"no content type", map[string]string{"Content-Type": ""}, string(body), 415},
		{"foreign origin", map[string]string{"Content-Type": j, "Origin": "https://evil.example"}, string(body), 403},
		{"cross-site null origin", map[string]string{"Content-Type": j, "Origin": "null", "Sec-Fetch-Site": "cross-site"}, string(body), 403},
		{"no edge secret", map[string]string{"Content-Type": j, EdgeHeader: ""}, string(body), 403},
		{"other user", map[string]string{"Content-Type": j, "Remote-User": "underarock"}, string(body), 403},
		{"forged token", map[string]string{"Content-Type": j}, `{"hash":"` + req.Hash + `","version":` + "1" + `,"token":"00"}`, 403},
		{"bad json", map[string]string{"Content-Type": j}, `{`, 400},
	}
	for _, c := range cases {
		if got := apiDo(h, "POST", path, c.headers, c.body).Code; got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	if f.sets != 0 {
		t.Fatal("a refused API post wrote")
	}
	if w := apiDo(h, "GET", "/api/v1/requests", map[string]string{"Remote-User": "underarock"}, ""); w.Code != 403 {
		t.Fatalf("list for another user: %d", w.Code)
	}
}

func TestSPAServedBehindTheSameGateWithItsOwnPolicy(t *testing.T) {
	f := newFake()
	s := NewServer(decider(f, &bytes.Buffer{}), []string{king}, "gluck", "https://order.kelliher.info", []byte(edgeSecret), []netip.Addr{netip.MustParseAddr("192.0.2.1")})
	s.SPA = fstest.MapFS{"index.html": {Data: []byte("<h1>spa</h1>")}, "app.js": {Data: []byte("1")}}
	h := s.Handler()
	w := apiDo(h, "GET", "/app/", nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "spa") {
		t.Fatalf("app: %d %s", w.Code, w.Body.String())
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "form-action 'none'") {
		t.Fatalf("spa policy: %s", csp)
	}
	if w := apiDo(h, "GET", "/app/app.js", map[string]string{EdgeHeader: ""}, ""); w.Code != 403 {
		t.Fatalf("spa without the edge secret: %d", w.Code)
	}
	if csp := apiDo(h, "GET", "/classic", nil, "").Header().Get("Content-Security-Policy"); strings.Contains(csp, "script-src") {
		t.Fatalf("the HTML fallback gained script rights: %s", csp)
	}
	plain := NewServer(decider(f, &bytes.Buffer{}), []string{king}, "gluck", "o", []byte(edgeSecret), []netip.Addr{netip.MustParseAddr("192.0.2.1")})
	if w := apiDo(plain.Handler(), "GET", "/app/", nil, ""); w.Code != 404 {
		t.Fatalf("app without --spa-dir: %d", w.Code)
	}
}

func TestSPAIsTheRootAndTheHTMLPageMovesToClassic(t *testing.T) {
	f := newFake()
	peers := []netip.Addr{netip.MustParseAddr("192.0.2.1")}
	s := NewServer(decider(f, &bytes.Buffer{}), []string{king}, "gluck", "https://order.kelliher.info", []byte(edgeSecret), peers)
	s.SPA = fstest.MapFS{
		"index.html":        {Data: []byte("<h1>spa</h1>")},
		"placet.js":         {Data: []byte("1")},
		"fonts/inter.woff2": {Data: []byte("f")},
	}
	h := s.Handler()
	for _, path := range []string{"/", "/placet.js", "/fonts/inter.woff2", "/app/"} {
		w := apiDo(h, "GET", path, nil, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
			t.Fatalf("%s lacks the spa policy: %s", path, csp)
		}
	}
	if body := apiDo(h, "GET", "/", nil, "").Body.String(); !strings.Contains(body, "spa") {
		t.Fatalf("root is not the spa: %s", body)
	}
	w := apiDo(h, "GET", "/classic", nil, "")
	if w.Code != 200 || strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src") {
		t.Fatalf("classic: %d %s", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	for _, path := range []string{"/", "/classic", "/placet.js", "/api/v1/requests"} {
		if w := apiDo(h, "GET", path, map[string]string{EdgeHeader: ""}, ""); w.Code != 403 {
			t.Fatalf("%s without the edge secret: %d", path, w.Code)
		}
	}
	plain := NewServer(decider(f, &bytes.Buffer{}), []string{king}, "gluck", "o", []byte(edgeSecret), peers)
	if body := apiDo(plain.Handler(), "GET", "/", nil, "").Body.String(); strings.Contains(body, "spa") || apiDo(plain.Handler(), "GET", "/classic", nil, "").Code != 404 {
		t.Fatal("without --spa-dir the root must stay the HTML page and /classic must not exist")
	}
}
