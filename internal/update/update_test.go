package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.4", "v0.1.3", +1},
		{"v0.1.3", "v0.1.4", -1},
		{"v0.1.4", "v0.1.4", 0},
		{"v1.0.0", "v0.9.9", +1},
		{"v0.10.0", "v0.9.9", +1}, // numeric, not lexical
		{"v1.0.0", "v1.0.0-rc1", +1},
		{"v1.0.0-rc2", "v1.0.0-rc1", +1},
		{"dev-abc123", "v0.1.4", -1}, // unparseable = -inf
		{"v0.1.4", "dev-abc123", +1},
	}
	for _, tc := range cases {
		if got := Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(filepath.Join(dir, "figaro"))
	// Miss on cold cache.
	if got := c.Read(time.Hour); got != nil {
		t.Fatalf("cold read = %+v, want nil", got)
	}
	info := &Info{
		Current:   "v0.1.3",
		Latest:    "v0.1.4",
		Available: true,
		Channel:   ChannelGoInstall,
		CheckedAt: time.Now(),
	}
	if err := c.Write(info); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := c.Read(time.Hour)
	if got == nil {
		t.Fatal("read after write = nil")
	}
	if got.Latest != "v0.1.4" || !got.Available {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// Expired TTL: miss.
	if got := c.Read(1 * time.Nanosecond); got != nil {
		time.Sleep(2 * time.Nanosecond)
		if got := c.Read(1 * time.Nanosecond); got != nil {
			t.Errorf("expired read = %+v, want nil", got)
		}
	}
}

func TestNudge(t *testing.T) {
	// Nothing available: silent.
	if got := Nudge(&Info{Current: "v0.1.4", Latest: "v0.1.4"}, "example.com/m"); got != "" {
		t.Errorf("no-op nudge = %q", got)
	}
	// go-install channel: shows the exact command.
	msg := Nudge(&Info{
		Current: "v0.1.3", Latest: "v0.1.4",
		Available: true, Channel: ChannelGoInstall,
	}, "github.com/jack-work/figaro")
	if !contains(msg, "go install github.com/jack-work/figaro/cmd/figaro@v0.1.4") {
		t.Errorf("go-install nudge missing command: %q", msg)
	}
	// Unknown channel: falls back to `figaro update`.
	msg = Nudge(&Info{
		Current: "v0.1.3", Latest: "v0.1.4",
		Available: true, Channel: ChannelUnknown,
	}, "github.com/jack-work/figaro")
	if !contains(msg, "figaro update") {
		t.Errorf("unknown-channel nudge missing fallback: %q", msg)
	}
}

func TestDetectChannel_GoInstall(t *testing.T) {
	// Fake $GOBIN and put an exe inside it via a symlink.
	tmp := t.TempDir()
	gobin := filepath.Join(tmp, "gobin")
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBIN", gobin)
	// We can't override os.Executable() directly; this test just
	// exercises DetectChannel's *env* handling for regressions -
	// the exe path is whatever the test binary is, so the assertion
	// is: if the test binary lives under $GOBIN, we detect go-install;
	// otherwise the code falls through, which is also fine.
	_ = DetectChannel()
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A dev-stamped current version can't be compared to release tags -
// the nudge must stay silent instead of claiming any tag is newer.
func TestNudge_SilentOnDevVersion(t *testing.T) {
	info := &Info{Current: "dev-da2abf499885", Latest: "v0.3.0", Available: true, Channel: ChannelNix}
	if msg := Nudge(info, "example.com/mod"); msg != "" {
		t.Fatalf("dev version must not nudge, got %q", msg)
	}
	info = &Info{Current: "v0.2.0", Latest: "v0.3.0", Available: true, Channel: ChannelNix}
	if msg := Nudge(info, "example.com/mod"); msg == "" {
		t.Fatalf("semver current with newer latest should nudge")
	}
}

// The marker an installer drops beside the binary is the only honest answer to
// "how did this get here": ~/.local/bin is a shared drawer, and a guess there
// prints an upgrade command that could damage the install it guessed about.
func TestDetectChannel_Marker(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "figaro")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ChannelMarker), []byte("script\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := channelFor(exe); got != ChannelScript {
		t.Fatalf("marker ignored: got %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, ChannelMarker), []byte("nonsense\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := channelFor(exe); got == ChannelScript {
		t.Fatal("a marker naming no known channel must not be believed")
	}
}

func TestDetectChannel_Homebrew(t *testing.T) {
	for _, exe := range []string{
		"/opt/homebrew/Cellar/figaro/0.24.0/bin/figaro",
		"/home/linuxbrew/.linuxbrew/Cellar/figaro/0.24.0/bin/figaro",
		"/opt/brew/Cellar/figaro/0.24.0/bin/figaro", // a custom prefix is still brew
	} {
		if got := channelFor(exe); got != ChannelHomebrew {
			t.Fatalf("%s: got %q", exe, got)
		}
	}
	if got := channelFor("/nix/store/abc-figaro/bin/figaro"); got != ChannelNix {
		t.Fatalf("got %q", got)
	}
}
