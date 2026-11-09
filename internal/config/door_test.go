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

// The web door is off unless configured, and the spain shape parses as written.
func TestWebDoorConfig(t *testing.T) {
	if _, ok, err := loadToml(t, "").WebDoor(); ok || err != nil {
		t.Fatalf("unconfigured web door: ok=%v err=%v", ok, err)
	}
	w, ok, err := loadToml(t, `[door.web]
path = "/run/figaro/web.sock"
mode = "0660"
max_conn_age = "8h"
authn = "upstream"
`).WebDoor()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	want := WebDoor{Path: "/run/figaro/web.sock", Mode: 0o660, MaxConnAge: 8 * time.Hour, Authn: "upstream"}
	if w != want {
		t.Fatalf("got %+v, want %+v", w, want)
	}
	w, _, _ = loadToml(t, "[door.web]\npath = \"/x.sock\"\n").WebDoor()
	if w.Mode != 0o660 || w.MaxConnAge != 8*time.Hour {
		t.Fatalf("defaults: %+v", w)
	}
}

// A web door that cannot be honoured exactly must stop the daemon, not open loosely.
func TestWebDoorConfigRefusesWhatItCannotHonour(t *testing.T) {
	for _, body := range []string{
		"[door.web]\nmode = \"0660\"\n",
		"[door.web]\npath = \"/x.sock\"\nmode = \"rw-rw----\"\n",
		"[door.web]\npath = \"/x.sock\"\nmode = \"1777\"\n",
		"[door.web]\npath = \"/x.sock\"\nmax_conn_age = \"forever\"\n",
		"[door.web]\npath = \"/x.sock\"\nmax_conn_age = \"0s\"\n",
	} {
		if _, _, err := loadToml(t, body).WebDoor(); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

// An edge follow lives an hour unless [door] says otherwise; anything but a
// positive duration is refused by name.
func TestEdgeFollowMaxAge(t *testing.T) {
	if d, err := loadToml(t, "").EdgeFollowMaxAge(); err != nil || d != time.Hour {
		t.Fatalf("unset: %v %v", d, err)
	}
	if d, err := loadToml(t, "[door]\nedge_follow_max_age = \"15m\"\n").EdgeFollowMaxAge(); err != nil || d != 15*time.Minute {
		t.Fatalf("15m: %v %v", d, err)
	}
	for _, bad := range []string{"0s", "-1m", "an hour"} {
		if _, err := loadToml(t, "[door]\nedge_follow_max_age = \""+bad+"\"\n").EdgeFollowMaxAge(); err == nil || !strings.Contains(err.Error(), "door.edge_follow_max_age") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// All door queues share 192 MiB unless [door] says otherwise; a negative
// size is refused by name.
func TestDoorOutboxBudget(t *testing.T) {
	if n, err := loadToml(t, "").DoorOutboxBudget(); err != nil || n != 192<<20 {
		t.Fatalf("unset: %v %v", n, err)
	}
	if n, err := loadToml(t, "[door]\noutbox_budget_mib = 64\n").DoorOutboxBudget(); err != nil || n != 64<<20 {
		t.Fatalf("64: %v %v", n, err)
	}
	if _, err := loadToml(t, "[door]\noutbox_budget_mib = -1\n").DoorOutboxBudget(); err == nil || !strings.Contains(err.Error(), "door.outbox_budget_mib") {
		t.Errorf("-1: %v", err)
	}
}
