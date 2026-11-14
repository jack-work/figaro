package config

import (
	"strings"
	"testing"
	"time"
)

func loadToml(t *testing.T, body string) *Loaded {
	t.Helper()
	l, err := loadWith(t, body)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The web endpoint is off unless configured, and the spain shape parses as written.
func TestWebEndpointConfig(t *testing.T) {
	if _, ok, err := loadToml(t, "").WebEndpoint(); ok || err != nil {
		t.Fatalf("unconfigured web endpoint: ok=%v err=%v", ok, err)
	}
	w, ok, err := loadToml(t, `[endpoint.web]
path = "/run/figaro/web.sock"
mode = "0660"
max_conn_age = "8h"
authn = "upstream"
`).WebEndpoint()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := WebEndpoint{Path: "/run/figaro/web.sock", Mode: 0o660, MaxConnAge: 8 * time.Hour, Authn: "upstream"}
	if w != want {
		t.Fatalf("got %+v, want %+v", w, want)
	}
	w, _, _ = loadToml(t, "[endpoint.web]\npath = \"/x.sock\"\n").WebEndpoint()
	if w.Mode != 0o660 || w.MaxConnAge != 8*time.Hour {
		t.Fatalf("defaults: %+v", w)
	}
}

// A web endpoint that cannot be honoured exactly must stop the daemon, not open loosely.
func TestWebEndpointConfigRefusesWhatItCannotHonour(t *testing.T) {
	for _, body := range []string{
		"[endpoint.web]\nmode = \"0660\"\n",
		"[endpoint.web]\npath = \"/x.sock\"\nmode = \"rw-rw----\"\n",
		"[endpoint.web]\npath = \"/x.sock\"\nmode = \"1777\"\n",
		"[endpoint.web]\npath = \"/x.sock\"\nmax_conn_age = \"forever\"\n",
		"[endpoint.web]\npath = \"/x.sock\"\nmax_conn_age = \"0s\"\n",
	} {
		if _, _, err := loadToml(t, body).WebEndpoint(); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

// An edge follow lives an hour unless [endpoint] says otherwise; anything but a
// positive duration is refused by name.
func TestEdgeFollowMaxAge(t *testing.T) {
	if d, err := loadToml(t, "").EdgeFollowMaxAge(); err != nil || d != time.Hour {
		t.Fatalf("unset: %v %v", d, err)
	}
	if d, err := loadToml(t, "[endpoint]\nedge_follow_max_age = \"15m\"\n").EdgeFollowMaxAge(); err != nil || d != 15*time.Minute {
		t.Fatalf("15m: %v %v", d, err)
	}
	for _, bad := range []string{"0s", "-1m", "an hour"} {
		if _, err := loadToml(t, "[endpoint]\nedge_follow_max_age = \""+bad+"\"\n").EdgeFollowMaxAge(); err == nil || !strings.Contains(err.Error(), "endpoint.edge_follow_max_age") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// All endpoint queues share 192 MiB unless [endpoint] says otherwise; a negative
// size is refused by name.
func TestEndpointOutboxBudget(t *testing.T) {
	if n, err := loadToml(t, "").EndpointOutboxBudget(); err != nil || n != 192<<20 {
		t.Fatalf("unset: %v %v", n, err)
	}
	if n, err := loadToml(t, "[endpoint]\noutbox_budget_mib = 64\n").EndpointOutboxBudget(); err != nil || n != 64<<20 {
		t.Fatalf("64: %v %v", n, err)
	}
	if _, err := loadToml(t, "[endpoint]\noutbox_budget_mib = -1\n").EndpointOutboxBudget(); err == nil || !strings.Contains(err.Error(), "endpoint.outbox_budget_mib") {
		t.Errorf("-1: %v", err)
	}
}
