package auth

import (
	"testing"

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
