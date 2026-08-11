package rpc

import "fmt"

// MaxAriaIDLen caps id length to fit unix socket sun_path.
const MaxAriaIDLen = 64

// ValidateAriaID enforces [A-Za-z0-9_-]{1,64}, with one addition: a
// leading '@': the unbound-form sigil. Node ids are one namespace
// (a figaro IS its bound form, addressed by one id), so every surface
// that validates an aria id must admit a form id too; the sigil is how
// a human and a verb tell the species apart, not a separate grammar.
// Legacy stump ids ("name@hash") carry an interior '@' and are also
// admitted: they are legacy forms.
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
		case r == '@':
		default:
			return fmt.Errorf("aria id: invalid char %q at position %d (allowed: [A-Za-z0-9_-@])", r, i)
		}
	}
	return nil
}
