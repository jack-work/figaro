package gateway

// THE IMPERSONATION TESTS.
//
// figaro trusts the calling aria's id on assertion -- honest for a unix
// socket whose security model is 0600, because a local process could claim
// any id anyway. Through a gateway that argument dies: what nothing stops a
// LOCAL process from claiming, nothing stops a REMOTE one from claiming.
//
// A remote caller that could name itself another aria would satisfy
// self-targeted policy rules and would appear in a transcript as a figaro
// the reader is told to trust. These tests are what stops that.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func params(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	return json.RawMessage(raw)
}

func decode(t *testing.T, b json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result is not an object: %s", b)
	}
	return m
}

// THE ONE THAT MATTERS. A forged aria id must not survive the gateway.
func TestForgedCallerIsStripped(t *testing.T) {
	in := params(t, `{"`+rpc.CallerKey+`":"aria-i-am-not","text":"hello"}`)
	out, err := rewriteEnvelope(in, authz.Identity{Authenticated: true, Label: "gluck"})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out)
	if v, ok := m[rpc.CallerKey]; ok {
		t.Fatalf("a forged %s survived the gateway as %v", rpc.CallerKey, v)
	}
	if m["text"] != "hello" {
		t.Fatalf("payload mangled: %v", m)
	}
}

// Stripping must happen even when the gateway has nothing to stamp back.
// Otherwise an anonymous listener would forward whatever it was handed.
func TestForgedCallerIsStrippedEvenWithNoIdentity(t *testing.T) {
	in := params(t, `{"`+rpc.CallerKey+`":"aria-i-am-not"}`)
	out, err := rewriteEnvelope(in, authz.Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decode(t, out)[rpc.CallerKey]; ok {
		t.Fatal("an anonymous gateway forwarded a forged caller id")
	}
}

// A forged LABEL is attribution, not authorization -- but it renders as a
// nametag the reader is told to trust, so it is replaced too.
func TestForgedLabelIsReplacedNotAppended(t *testing.T) {
	in := params(t, `{"`+rpc.CallerLabelKey+`":"DUKE"}`)
	out, err := rewriteEnvelope(in, authz.Identity{Authenticated: true, Label: "gluck"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decode(t, out)[rpc.CallerLabelKey].(string)
	if strings.Contains(got, "DUKE") {
		t.Fatalf("a forged label survived: %q", got)
	}
	if !strings.Contains(got, "gluck") {
		t.Fatalf("the authenticated caller is not attributed: %q", got)
	}
}

// EVERY OTHER FIELD SURVIVES BYTE-EXACT. This is the promise that makes the
// rewrite legitimate rather than a second copy of the API: the gateway owns
// the attribution keys and nothing else, including keys it has never heard of.
func TestUnknownParamsSurviveTheRewrite(t *testing.T) {
	in := params(t, `{"`+rpc.CallerKey+`":"x","future_field":{"deep":[1,2,{"a":null}]},"n":3.25}`)
	out, err := rewriteEnvelope(in, authz.Identity{Label: "gluck"})
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, out)
	raw, _ := json.Marshal(m["future_field"])
	if string(raw) != `{"deep":[1,2,{"a":null}]}` {
		t.Fatalf("unknown field altered: %s", raw)
	}
	if m["n"] != 3.25 {
		t.Fatalf("number altered: %v", m["n"])
	}
}

// Methods that send nil or a non-object must pass through rather than being
// coerced into a shape the daemon does not expect.
func TestNonObjectParamsPassThrough(t *testing.T) {
	for _, in := range []string{``, `null`, `[1,2,3]`, `"a string"`} {
		out, err := rewriteEnvelope(json.RawMessage(in), authz.Identity{Label: "g"})
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if in == `[1,2,3]` || in == `"a string"` {
			if string(out) != in {
				t.Fatalf("%q was rewritten to %q", in, out)
			}
		}
	}
}

// ── authenticators ───────────────────────────────────────────────────────

func TestUpstreamAuthRefusesWithoutRemoteUser(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/socket", nil)
	if _, err := (upstreamAuth{}).authenticate(r); err == nil {
		t.Fatal("a request with no Remote-User was authenticated: " +
			"its absence means we are not behind the proxy we think we are")
	}
}

func TestUpstreamAuthReadsGroups(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/socket", nil)
	r.Header.Set("Remote-User", "gluck")
	r.Header.Set("Remote-Groups", " figaro-admin , keel-admin ")
	id, err := (upstreamAuth{}).authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Authenticated || id.Label != "gluck" {
		t.Fatalf("identity = %+v", id)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "figaro-admin" {
		t.Fatalf("groups = %v", id.Groups)
	}
}

func TestDoorkeyAuth(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	a := doorkeyAuth{secret: secret}

	t.Run("correct token", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/v1/socket", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		id, err := a.authenticate(r)
		if err != nil || !id.Authenticated {
			t.Fatalf("valid token refused: %v", err)
		}
	})

	for _, bad := range []struct{ name, hdr string }{
		{"absent", ""},
		{"wrong", "Bearer 00000000000000000000000000000000"},
		{"prefix only", "Bearer " + secret[:8]},
		{"not bearer", secret},
		{"basic", "Basic " + secret},
	} {
		t.Run(bad.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/v1/socket", nil)
			if bad.hdr != "" {
				r.Header.Set("Authorization", bad.hdr)
			}
			if _, err := a.authenticate(r); err == nil {
				t.Fatalf("%s was accepted", bad.name)
			}
		})
	}

	// A token in a query string lands in access logs, Referer headers and
	// browser history. It must not be a way in.
	t.Run("query parameter is not accepted", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/v1/socket?token="+secret, nil)
		if _, err := a.authenticate(r); err == nil {
			t.Fatal("a token in the query string authenticated")
		}
	})
}

// A refusal must be an HTTP STATUS, not a WebSocket that opens and closes:
// the second is indistinguishable from a network fault, and a client will
// retry it forever.
func TestUnauthorizedUpgradeIsAStatusNotASocket(t *testing.T) {
	sock := echoDaemon(t)
	srv := httptest.NewServer(&Tunnel{
		Dial:  UnixDialer(sock),
		Authn: doorkeyAuth{secret: "0123456789abcdef0123456789abcdef"},
	})
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/socket", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
