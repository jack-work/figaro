package approvals

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func serve(t *testing.T) (*fakeForms, http.Handler) {
	t.Helper()
	f := newFake()
	req := modelReq(seat)
	req["reason"] = `<script>alert(1)</script>`
	f.file(t, king, "r1", req)
	s := NewServer(decider(f, &bytes.Buffer{}), []string{king}, "gluck", "https://order.kelliher.info")
	return f, s.Handler()
}

func do(h http.Handler, method, path, user, origin string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if user != "" {
		r.Header.Set("Remote-User", user)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func hidden(body, name string) string {
	m := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestPageRefusesNonAdmin(t *testing.T) {
	_, h := serve(t)
	if w := do(h, "GET", "/", "", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("no user: %d", w.Code)
	}
	if w := do(h, "GET", "/", "underarock", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("other user: %d", w.Code)
	}
}

func TestDetailEscapesAndApproveRoundTrip(t *testing.T) {
	f, h := serve(t)
	w := do(h, "GET", "/r/"+url.PathEscape(king)+"/r1", "gluck", "", nil)
	body := w.Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatal("request text rendered unescaped")
	}
	if !strings.Contains(body, `set system.model = &#34;claude-opus-4-5&#34;`) {
		t.Fatalf("exact patch line missing:\n%s", body)
	}
	form := url.Values{}
	for _, k := range []string{"filing", "id", "hash", "version", "token"} {
		form.Set(k, hidden(body, k))
	}
	if w := do(h, "POST", "/approve", "gluck", "https://evil.example", form); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin approve: %d", w.Code)
	}
	bad := url.Values{}
	for k, v := range form {
		bad[k] = v
	}
	bad.Set("hash", strings.Repeat("a", 64))
	if w := do(h, "POST", "/approve", "gluck", "https://order.kelliher.info", bad); w.Code != http.StatusForbidden {
		t.Fatalf("hash outside the token: %d", w.Code)
	}
	w = do(h, "POST", "/approve", "gluck", "https://order.kelliher.info", form)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "applied") {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	if get(t, f, seat, "system.model") != `"claude-opus-4-5"` {
		t.Fatal("not applied")
	}
	if w := do(h, "POST", "/approve", "gluck", "https://order.kelliher.info", form); w.Code != http.StatusConflict {
		t.Fatalf("second click: %d", w.Code)
	}
}
