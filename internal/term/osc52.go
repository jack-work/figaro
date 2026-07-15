package term

import "encoding/base64"

// OSC52 returns the OSC 52 escape that copies s to the system clipboard. It is
// terminal-native (works over SSH, tmux with set-clipboard on, and Windows
// Terminal), so no shell-out (xclip/pbcopy/clip.exe) is needed.
func OSC52(s string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07"
}
