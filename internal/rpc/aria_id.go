package rpc

import "fmt"

// MaxAriaIDLen is the longest accepted aria id. Capped so that the
// derived unix socket path (`$XDG_RUNTIME_DIR/figaro/figaros/<id>.sock`)
// stays comfortably under the 108-byte sun_path limit on Linux.
const MaxAriaIDLen = 64

// ValidateAriaID enforces the character set and length policy for
// caller-supplied aria ids. Allowed: ASCII letters, digits, underscore,
// hyphen. Length: 1..64. The id becomes both a filesystem directory
// component and a unix socket filename, so anything that could cause
// path traversal or shell quoting trouble is rejected.
func ValidateAriaID(id string) error {
	if len(id) == 0 {
		return fmt.Errorf("aria id is empty")
	}
	if len(id) > MaxAriaIDLen {
		return fmt.Errorf("aria id too long: %d chars (max %d)", len(id), MaxAriaIDLen)
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("aria id: invalid char %q at position %d (allowed: [A-Za-z0-9_-])", r, i)
		}
	}
	return nil
}
