package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	hushconfig "github.com/jack-work/hush/config"
	"github.com/jack-work/hush/managed"
)

// newTestVault builds a managed hush rooted entirely under t.TempDir():
// its own identity, config and socket dir. Nothing here touches the
// user's real vault, and no agent is started.
func newTestVault(t *testing.T) *managed.Hush {
	t.Helper()
	root := t.TempDir()
	h, err := managed.New(managed.Options{
		AppName: "figaro-test",
		Mode:    managed.ModeEmbedded,
		Dirs: &hushconfig.Dirs{
			ConfigDir:  filepath.Join(root, "config"),
			StateDir:   filepath.Join(root, "state"),
			RuntimeDir: filepath.Join(root, "run"),
		},
	})
	if err != nil {
		t.Fatalf("managed.New: %v", err)
	}
	return h
}

// The point of the file arrangement is that the NEXT process, which
// knows nothing but hush.toml, unlocks without asking anyone anything.
// So the test resolves the config from disk the way that process would.
func TestSetupFileUnlockIsReadBackByHush(t *testing.T) {
	h := newTestVault(t)
	pp, path, err := setupFileUnlock(h)
	if err != nil {
		t.Fatalf("setupFileUnlock: %v", err)
	}
	if len(pp) < 40 {
		t.Fatalf("passphrase is %d bytes, want 32 random bytes base64-encoded", len(pp))
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("passphrase file mode is %#o, want 0600", perm)
	}

	cfg, err := hushconfig.LoadWithDirs(hushconfig.Dirs{
		ConfigDir:  h.Config().ConfigDir,
		StateDir:   h.Config().StateDir,
		RuntimeDir: h.Config().RuntimeDir,
	})
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Unlock.Method != "file" {
		t.Fatalf("reloaded method = %q, want file", cfg.Unlock.Method)
	}
	if cfg.Unlock.File != path {
		t.Fatalf("reloaded file = %q, want %q", cfg.Unlock.File, path)
	}

	// And the identity it creates opens with the passphrase in the file.
	if _, err := h.Init(pp); err != nil {
		t.Fatalf("Init: %v", err)
	}
	fromFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fromFile = []byte(strings.TrimSuffix(string(fromFile), "\n"))
	if err := h.VerifyPassphrase(fromFile); err != nil {
		t.Fatalf("the passphrase in the file does not open the identity: %v", err)
	}
}

// figaro sets a long hush ttl on purpose (a 30m default kills the
// credential in the middle of an hour-long turn). Writing the unlock
// section must not cost that.
func TestWriteHushUnlockFileKeepsWhatIsAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hush.toml")
	existing := "ttl = \"168h\"\nidentity = \"/somewhere/identity.age\"\n\n[unlock]\nmethod = \"keyring\"\n\n[unlock.keyring]\nservice = \"figaro\"\naccount = \"default\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeHushUnlockFile(path, "/home/someone/.config/figaro/hush/passphrase"); err != nil {
		t.Fatalf("writeHushUnlockFile: %v", err)
	}

	var doc struct {
		TTL      string `toml:"ttl"`
		Identity string `toml:"identity"`
		Unlock   struct {
			Method  string `toml:"method"`
			File    string `toml:"file"`
			Keyring struct {
				Service string `toml:"service"`
			} `toml:"keyring"`
		} `toml:"unlock"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("reparse: %v\n%s", err, data)
	}
	if doc.TTL != "168h" {
		t.Errorf("ttl = %q, want 168h to survive", doc.TTL)
	}
	if doc.Identity != "/somewhere/identity.age" {
		t.Errorf("identity = %q, want it to survive", doc.Identity)
	}
	if doc.Unlock.Method != "file" {
		t.Errorf("method = %q, want file", doc.Unlock.Method)
	}
	if doc.Unlock.File != "/home/someone/.config/figaro/hush/passphrase" {
		t.Errorf("file = %q", doc.Unlock.File)
	}
	if doc.Unlock.Keyring.Service != "figaro" {
		t.Errorf("keyring service = %q, want it to survive", doc.Unlock.Keyring.Service)
	}
}

func TestWriteHushUnlockFileCreatesOneWhenThereIsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hush.toml")
	if err := writeHushUnlockFile(path, "/tmp/pp"); err != nil {
		t.Fatalf("writeHushUnlockFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("hush.toml mode is %#o, want 0600", perm)
	}
}

// vault reset must know which providers to send the user back through
// BEFORE it moves their token files, since afterwards there is nothing
// left to read.
func TestVaultOAuthProvidersListsTokenFiles(t *testing.T) {
	h := newTestVault(t)
	oauthDir := filepath.Join(h.Config().StateDir, "oauth")
	if err := os.MkdirAll(oauthDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"anthropic.toml", "copilot.toml", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(oauthDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := vaultOAuthProviders(h)
	if len(got) != 2 || got[0] != "anthropic" || got[1] != "copilot" {
		t.Fatalf("providers = %v, want [anthropic copilot]", got)
	}
}

func TestArchiveVaultFilesRenamesRatherThanDeletes(t *testing.T) {
	h := newTestVault(t)
	pp, _, err := setupFileUnlock(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Init(pp); err != nil {
		t.Fatalf("Init: %v", err)
	}
	oauthDir := filepath.Join(h.Config().StateDir, "oauth")
	if err := os.MkdirAll(oauthDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oauthDir, "anthropic.toml"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}

	moved, err := archiveVaultFiles(h, []string{"anthropic"}, "20260910-180000")
	if err != nil {
		t.Fatalf("archiveVaultFiles: %v", err)
	}
	if len(moved) < 2 {
		t.Fatalf("moved %v, want the identity and the token file", moved)
	}
	for _, m := range moved {
		if !strings.HasSuffix(m, ".reset-20260910-180000") {
			t.Errorf("%s does not carry the stamp", m)
		}
		if _, err := os.Stat(m); err != nil {
			t.Errorf("%s should still exist: %v", m, err)
		}
	}
	if h.HasIdentity() {
		t.Error("the identity should be out of the way after archiving")
	}
	if _, err := os.Stat(filepath.Join(oauthDir, "anthropic.toml")); !os.IsNotExist(err) {
		t.Error("the token file should be out of the way after archiving")
	}
}

func TestVaultUnlockKindFromFlags(t *testing.T) {
	if k, err := vaultUnlockKindFromFlags(false, false); err != nil || k != unlockKindAuto {
		t.Errorf("neither flag: (%q, %v)", k, err)
	}
	if k, err := vaultUnlockKindFromFlags(true, false); err != nil || k != unlockKindFile {
		t.Errorf("--file: (%q, %v)", k, err)
	}
	if k, err := vaultUnlockKindFromFlags(false, true); err != nil || k != unlockKindPassphrase {
		t.Errorf("--passphrase: (%q, %v)", k, err)
	}
	if _, err := vaultUnlockKindFromFlags(true, true); err == nil {
		t.Error("both flags should be refused, not silently resolved")
	}
}
