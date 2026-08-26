package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// The trailing newline is the whole reason this trims. Every mechanism that
// writes a credential file -- a heredoc, an editor, echo, sops -- leaves one,
// and a bearer with \n on the end fails authentication in a way that reads
// exactly like a wrong key.
func TestFileValueTrimsWhitespace(t *testing.T) {
	for _, body := range []string{"sk-abc123", "sk-abc123\n", "  sk-abc123  \n\n", "sk-abc123\r\n"} {
		f := &FileValue{Path: write(t, "k", body, 0o600)}
		got, ok, err := f.TryResolve()
		if err != nil || !ok {
			t.Fatalf("%q: ok=%v err=%v", body, ok, err)
		}
		if got != "sk-abc123" {
			t.Fatalf("%q resolved to %q", body, got)
		}
	}
}

// ABSENT IS NOT AN ERROR. This strategy sits in an Aggregate beside env vars
// and hush; a config that names no file must fall through so a workstation
// keeps using the credentials a person owns.
func TestFileValueAbsentFallsThrough(t *testing.T) {
	for _, p := range []string{"", filepath.Join(t.TempDir(), "nope")} {
		f := &FileValue{Path: p}
		_, ok, err := f.TryResolve()
		if ok {
			t.Fatalf("%q reported a credential", p)
		}
		if err != nil {
			t.Fatalf("%q errored instead of falling through: %v", p, err)
		}
	}
}

// PRESENT BUT UNREADABLE IS AN ERROR, and must not fall through. The usual
// cause is a credential mode or owner that does not match the service user,
// and falling through would surface it as "no credential available" three
// layers from the mistake.
func TestFileValueUnreadableIsLoud(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	p := write(t, "k", "sk-abc", 0o000)
	f := &FileValue{Path: p}
	_, ok, err := f.TryResolve()
	if ok {
		t.Fatal("an unreadable file yielded a credential")
	}
	if err == nil {
		t.Fatal("an unreadable credential file fell through silently")
	}
	if !strings.Contains(err.Error(), p) {
		t.Fatalf("error does not name the path: %v", err)
	}
}

// An empty file is a provisioning failure, not an empty credential.
func TestFileValueEmptyIsAnError(t *testing.T) {
	f := &FileValue{Path: write(t, "k", "\n  \n", 0o600)}
	_, ok, err := f.TryResolve()
	if ok || err == nil {
		t.Fatalf("an empty credential file was accepted: ok=%v err=%v", ok, err)
	}
}

// The file is the source of truth; this process does not get to decide it is
// wrong. Rotation is the operator's job, so Invalidate must not delete it.
func TestFileValueInvalidateLeavesTheFile(t *testing.T) {
	p := write(t, "k", "sk-abc", 0o600)
	f := &FileValue{Path: p}
	if err := f.Invalidate("sk-abc"); err != nil {
		t.Fatalf("Invalidate errored: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("Invalidate touched the credential file: %v", err)
	}
}
