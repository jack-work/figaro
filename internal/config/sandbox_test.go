package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoxesLoadAndRefuseWhatTheyCannotBe(t *testing.T) {
	for _, c := range []struct {
		body string
		ok   bool
	}{
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\nendpoint = \"/run/endpoint/judge.sock\"\nprofile = \"build\"\n", true},
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\n", true},
		{"[sandbox.boxes.local]\nexec = \"/x\"\n", false},
		{"[sandbox.boxes.\"a@b\"]\nexec = \"/x\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"judge.sock\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\nendpoint = \"endpoint.sock\"\n", false},
		{"[sandbox.boxes.a]\nexec = \"/x\"\nendpoint = \"/d\"\n[sandbox.boxes.b]\nexec = \"/y\"\nendpoint = \"/d/\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\npurge_grace = \"1h\"\n", true},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\npurge_grace = \"an hour\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\npurge_grace = \"-1h\"\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\noutfits = [\"*\"]\nproviders = [\"*\"]\n", true},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\noutfits = [\"*\", \"judged\"]\n", false},
		{"[sandbox.boxes.judge]\nexec = \"/x\"\nproviders = [\"mock\", \"*\"]\n", false},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := Load(dir)
		if (err == nil) != c.ok {
			t.Errorf("%q: %v", c.body, err)
			continue
		}
		if c.ok {
			if b, ok := l.Box("judge"); ok && b.Exec != "/run/box/judge.sock" {
				t.Errorf("%q: box %+v", c.body, b)
			}
		}
	}
}

// A box named bwrap would silently run every form left on the removed bwrap
// provider, so the name stays reserved.
func TestABoxNamedBwrapIsRefusedAtLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[sandbox.boxes.bwrap]\nexec = \"/run/box/bwrap.sock\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "sandbox.boxes.bwrap") {
		t.Fatalf("a box named bwrap loaded: %v", err)
	}
}

// A box with an endpoint without both lists could mint undressed or on any provider,
// so its endpoint must stay shut; a box with no endpoint needs no lists.
func TestABoxEndpointNeedsBothLists(t *testing.T) {
	for _, c := range []struct {
		extra    string
		endpoint bool
		refused  string
	}{
		{"", true, "outfits or providers"},
		{"outfits = [\"judged\"]\n", true, "providers"},
		{"providers = [\"mock\"]\n", true, "outfits"},
		{"outfits = [\"judged\"]\nproviders = [\"mock\"]\n", true, ""},
		{"outfits = [\"*\"]\nproviders = [\"*\"]\n", true, ""},
		{"outfits = [\"*\"]\n", true, "providers"},
		{"", false, ""},
	} {
		body := "[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\n" + c.extra
		if c.endpoint {
			body += "endpoint = \"/run/endpoint/judge.sock\"\n"
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := Load(dir)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		err = l.BoxEndpointWithoutLists("judge")
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("%q: refused: %v", body, err)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), "no "+c.refused+" list")):
			t.Errorf("%q: want the endpoint refused for want of %s, got %v", body, c.refused, err)
		}
	}
}

func TestAHostEndpointLivesWhereNoBoxCanReach(t *testing.T) {
	const chat = "[sandbox.boxes.chat]\nexec = \"/run/sb/chat/exec/exec.sock\"\nendpoint = \"/run/sb/chat/endpoint/endpoint.sock\"\n"
	const receipt = "[sandbox.boxes.receipt]\nexec = \"/run/sb/receipt/exec/exec.sock\"\nendpoint = \"/run/sb/receipt/endpoint/endpoint.sock\"\n"
	for _, c := range []struct {
		name, body string
		refusal    string
	}{
		{"its own directory", chat + "host_endpoint = \"/run/sb/chat/host-endpoint/host.sock\"\nfigaro_claim = \"required\"\n", ""},
		{"no endpoint", "[sandbox.boxes.chat]\nexec = \"/x\"\nhost_endpoint = \"/run/sb/chat/host-endpoint/host.sock\"\n", "has none"},
		{"relative", chat + "host_endpoint = \"host.sock\"\n", "absolute"},
		{"its own endpoint directory", chat + "host_endpoint = \"/run/sb/chat/endpoint/host.sock\"\n", "endpoint directory"},
		{"below its own endpoint directory", chat + "host_endpoint = \"/run/sb/chat/endpoint/h/host.sock\"\n", "endpoint directory"},
		{"another box's endpoint directory", chat + "host_endpoint = \"/run/sb/receipt/endpoint/host.sock\"\n" + receipt, "box receipt's endpoint directory"},
		{"the endpoint itself", chat + "host_endpoint = \"/run/sb/chat/endpoint/endpoint.sock\"\n", "also"},
		{"another box's host endpoint", chat + "host_endpoint = \"/run/sb/h/host.sock\"\n" + receipt + "host_endpoint = \"/run/sb/h/host.sock\"\n", "also"},
		{"a claim without an endpoint", "[sandbox.boxes.chat]\nexec = \"/x\"\nfigaro_claim = \"required\"\n", "no endpoint"},
		{"an unknown claim", chat + "figaro_claim = \"optional\"\n", "want \"required\""},
		{"the retired node-wide claim", "[authz]\nfigaro_claim = \"required\"\n", "per box"},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := Load(dir)
		switch {
		case c.refusal == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.refusal != "" && (err == nil || !strings.Contains(err.Error(), c.refusal)):
			t.Errorf("%s: %v, want a refusal naming %q", c.name, err, c.refusal)
		case c.refusal == "":
			if got := l.HostEndpoints()["chat"]; got != "/run/sb/chat/host-endpoint/host.sock" {
				t.Errorf("%s: host endpoint %q", c.name, got)
			}
			if got := l.BoxesRequiringClaim(); len(got) != 1 || got[0] != "chat" {
				t.Errorf("%s: required %v", c.name, got)
			}
		}
	}
}
