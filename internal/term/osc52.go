package term

import "encoding/base64"

// OSC52 returns the escape that copies s to the system clipboard.
func OSC52(s string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) + "\x07"
}
