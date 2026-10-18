package boot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
)

func troupeConfig(t *testing.T, name, body string) *config.Loaded {
	t.Helper()
	cfgDir, _ := serverEnv(t)
	t.Setenv("FIGARO_TROUPE", "")
	if err := os.MkdirAll(filepath.Join(cfgDir, "troupes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if name != "" {
		writeFile(t, filepath.Join(cfgDir, "troupes", name+".toml"), body)
	}
	return &config.Loaded{ConfigDir: cfgDir}
}

const spainTroupe = `url = "wss://fig.kelliher.info"
[auth]
scheme = "oidc-device"
issuer = "https://auth.kelliher.info"
client_id = "figaro-cli"
`

func TestNoTroupeConfiguredMeansLocal(t *testing.T) {
	l := troupeConfig(t, "", "")
	if _, ok, err := SelectedTroupe(l); ok || err != nil {
		t.Fatalf("ok=%v err=%v with nothing configured", ok, err)
	}
}

func TestTroupeSelectedByConfigAndEnvironment(t *testing.T) {
	l := troupeConfig(t, "spain", spainTroupe)
	l.Config.CLI.Troupe = "spain"
	tr, ok, err := SelectedTroupe(l)
	if err != nil || !ok || tr.URL != "wss://fig.kelliher.info" || tr.Auth == nil || tr.Auth.ClientID != "figaro-cli" || tr.Auth.Issuer != "https://auth.kelliher.info" {
		t.Fatalf("config selection = %+v ok=%v err=%v", tr, ok, err)
	}
	if tr.HushCredential() != "troupe:spain" {
		t.Fatalf("hush credential = %q", tr.HushCredential())
	}

	t.Setenv("FIGARO_TROUPE", "unix:///run/figaro-door/angelus.sock")
	tr, ok, err = SelectedTroupe(l)
	if err != nil || !ok || tr.URL != "unix:///run/figaro-door/angelus.sock" {
		t.Fatalf("env selection = %+v ok=%v err=%v: the environment outranks the config", tr, ok, err)
	}
	if TroupeResolver(tr) != nil {
		t.Fatal("a unix troupe authenticates by peer credentials, not a bearer")
	}
}

func TestTroupeRefusals(t *testing.T) {
	l := troupeConfig(t, "plain", `url = "http://fig.kelliher.info"`+"\n")
	for _, sel := range []string{"plain", "missing", "../x", "https://fig.kelliher.info"} {
		t.Setenv("FIGARO_TROUPE", sel)
		if _, _, err := SelectedTroupe(l); err == nil {
			t.Errorf("FIGARO_TROUPE=%s was accepted", sel)
		}
	}
}

// The scheme names how a human signs in; one figaro does not know is refused
// when the file is read, not when a login half-runs.
func TestTroupeAuthSchemeIsCheckedAtLoad(t *testing.T) {
	for body, want := range map[string]string{
		"url = \"wss://a\"\n[auth]\nscheme = \"saml\"\nissuer = \"https://i\"\n": `auth.scheme "saml"`,
		"url = \"wss://a\"\n[auth]\nissuer = \"https://i\"\n":                    `auth.scheme ""`,
	} {
		l := troupeConfig(t, "x", body)
		if _, err := LoadTroupe(l, "x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %s", body, err, want)
		}
	}
}

// endpoints/ used [oauth]; a troupe file carrying it is refused by name
// rather than read as a troupe with no way to sign in.
func TestTroupeRefusesKeysItDoesNotKnow(t *testing.T) {
	l := troupeConfig(t, "old", "url = \"wss://a\"\n[oauth]\nissuer = \"https://i\"\n")
	if _, err := LoadTroupe(l, "old"); err == nil || !strings.Contains(err.Error(), "unknown key oauth") {
		t.Fatalf("err = %v", err)
	}
}

func TestTroupeNamesAreTheFilesInTroupes(t *testing.T) {
	l := troupeConfig(t, "spain", spainTroupe)
	writeFile(t, filepath.Join(l.TroupesDir(), "home.toml"), "url = \"unix:///run/x.sock\"\n")
	writeFile(t, filepath.Join(l.TroupesDir(), "notes.txt"), "")
	names, err := TroupeNames(l)
	if err != nil || strings.Join(names, ",") != "home,spain" {
		t.Fatalf("names = %v, %v", names, err)
	}
}

type fakeWorkload struct{ aud string }

func (f *fakeWorkload) Token(aud string) (string, error) { f.aud = aud; return "wl-token", nil }

// Inside a box the workload source answers first and hush is never reached.
func TestTroupeBearerPrefersTheWorkloadSource(t *testing.T) {
	l := troupeConfig(t, "spain", spainTroupe)
	fw := &fakeWorkload{}
	WorkloadSource = fw
	t.Cleanup(func() { WorkloadSource = nil })

	tr, err := LoadTroupe(l, "spain")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := TroupeResolver(tr).Resolve()
	if err != nil || tok != "wl-token" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	if fw.aud != "wss://fig.kelliher.info" {
		t.Fatalf("audience = %q, want the troupe URL by default", fw.aud)
	}
	assertHushUntouched(t)
}

func TestTroupeWithoutAnySourceSaysSo(t *testing.T) {
	l := troupeConfig(t, "bare", `url = "wss://fig.kelliher.info"`+"\n")
	tr, _ := LoadTroupe(l, "bare")
	_, err := TroupeResolver(tr).Resolve()
	if err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Fatalf("err = %v", err)
	}
	assertHushUntouched(t)
}
