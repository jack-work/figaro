package config

import (
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
