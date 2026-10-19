package boot

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/apikey"
)

func TestAnAPIKeyTroupeLoadsAndItsRefusalsAreNamed(t *testing.T) {
	ok := "url = \"wss://fig.example\"\n[auth]\nscheme = \"api-key\"\n"
	l := troupeConfig(t, "ci", ok)
	tr, err := LoadTroupe(l, "ci")
	if err != nil || !tr.APIKey() || tr.StoredKeyPath() != filepath.Join(l.TroupesDir(), "ci.key") {
		t.Fatalf("%+v %v", tr, err)
	}
	for body, want := range map[string]string{
		ok + "issuer = \"https://i\"\n":                                                                    "takes key_file only",
		ok + "key_file = \"relative\"\n":                                                                   "absolute",
		"url = \"unix:///run/x.sock\"\n[auth]\nscheme = \"api-key\"\n":                                     "over wss only",
		"url = \"wss://a\"\n[auth]\nscheme = \"oidc-device\"\nissuer = \"https://i\"\nkey_file = \"/k\"\n": "belongs to scheme",
	} {
		l := troupeConfig(t, "x", body)
		if _, err := LoadTroupe(l, "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %s", body, err, want)
		}
	}
}

func keyFileTroupe(t *testing.T, mode os.FileMode, body string) Troupe {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	l := troupeConfig(t, "ci", "url = \"wss://fig.example\"\n[auth]\nscheme = \"api-key\"\nkey_file = \""+path+"\"\n")
	tr, err := LoadTroupe(l, "ci")
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// A key_file answers before hush, and a workload token is never sent to a
// troupe that signs in with a key.
func TestAKeyFileIsTheBearerAndTheWorkloadSourceIsNotAsked(t *testing.T) {
	wire, _, err := apikey.New(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fw := &fakeWorkload{}
	WorkloadSource = fw
	t.Cleanup(func() { WorkloadSource = nil })
	tr := keyFileTroupe(t, 0o600, wire+"\n")
	tok, err := TroupeResolver(tr).Resolve()
	if err != nil || tok != wire {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	if fw.aud != "" {
		t.Fatalf("the workload source was asked for %q", fw.aud)
	}
	if err := TroupeResolver(tr).Invalidate(tok); err == nil || !strings.Contains(err.Error(), "troupe login ci") {
		t.Fatalf("Invalidate = %v", err)
	}
	assertHushUntouched(t)
}

func TestAKeyFileOthersCanReadIsRefused(t *testing.T) {
	wire, _, err := apikey.New(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tr := keyFileTroupe(t, 0o640, wire)
	if _, err := TroupeResolver(tr).Resolve(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
	tr = keyFileTroupe(t, 0o600, "sk-not-ours")
	if _, err := TroupeResolver(tr).Resolve(); err == nil || !strings.Contains(err.Error(), "not a figaro api key") {
		t.Fatalf("err = %v", err)
	}
	assertHushUntouched(t)
}

// With nothing stored the resolver says to log in, and hush is not started
// to find that out.
func TestAnAPIKeyTroupeWithNothingStoredSaysToLogIn(t *testing.T) {
	l := troupeConfig(t, "ci", "url = \"wss://fig.example\"\n[auth]\nscheme = \"api-key\"\n")
	tr, _ := LoadTroupe(l, "ci")
	if _, err := TroupeResolver(tr).Resolve(); err == nil || !strings.Contains(err.Error(), "troupe login ci") {
		t.Fatalf("err = %v", err)
	}
	writeFile(t, tr.StoredKeyPath(), "fgk_plaintext\n")
	if _, err := TroupeResolver(tr).Resolve(); err == nil || !strings.Contains(err.Error(), "no sealed key") {
		t.Fatalf("a plaintext key file was read: %v", err)
	}
	assertHushUntouched(t)
}
