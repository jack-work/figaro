package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// $BROWSER is tried first, as a command line taking the URL last or in
// place of %s; the platform opener stays behind it for when it will not
// start, which is what a WSL or headless box without xdg-open needs.
func TestTheBrowserIsTriedBeforeThePlatformOpener(t *testing.T) {
	const u = "https://idm.example/ui/oauth2?x=1"
	assert.Equal(t, [][]string{{"xdg-open", u}}, browserCommands("", "linux", u))
	assert.Equal(t, [][]string{{"wslview", u}, {"xdg-open", u}}, browserCommands("wslview", "linux", u))
	assert.Equal(t, [][]string{{"firefox", "--new-window", u}, {"open", u}}, browserCommands(" firefox --new-window ", "darwin", u))
	assert.Equal(t, [][]string{{"chromium", "--app=" + u}, {"rundll32", "url.dll,FileProtocolHandler", u}}, browserCommands("chromium --app=%s", "windows", u))
}

func TestABrowserOpensOnlyWithADisplayAndNoOptOut(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	assert.False(t, browserAllowed(env(), "linux"), "no display")
	assert.True(t, browserAllowed(env("DISPLAY", ":0"), "linux"))
	assert.True(t, browserAllowed(env("WAYLAND_DISPLAY", "wayland-1"), "linux"))
	assert.False(t, browserAllowed(env("DISPLAY", ":0", NoBrowserEnv, "1"), "linux"))
	assert.True(t, browserAllowed(env(), "darwin"))
	assert.False(t, browserAllowed(env(NoBrowserEnv, "1"), "darwin"))
}

func TestOpenBrowserLaunchesNothingWhenOptedOut(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "launched")
	script := filepath.Join(dir, "browser")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" > "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", script)
	t.Setenv("DISPLAY", ":0")

	t.Setenv(NoBrowserEnv, "1")
	assert.False(t, OpenBrowser("https://example.invalid/a"))
	time.Sleep(100 * time.Millisecond)
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "opted out, yet the browser command ran")

	t.Setenv(NoBrowserEnv, "")
	assert.True(t, OpenBrowser("https://example.invalid/b"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(marker); err == nil && string(b) == "https://example.invalid/b\n" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the control never launched the browser command, so the opt-out proves nothing")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
