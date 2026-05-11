package rpc

import "fmt"

// MaxAriaIDLen caps id length to fit unix socket sun_path.
const MaxAriaIDLen = 64

// ValidateAriaID enforces [A-Za-z0-9_-]{1,64}.
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
