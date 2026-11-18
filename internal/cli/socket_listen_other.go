//go:build !linux

package cli

func socketListening(string) (known, listening bool) {
	return false, false
}
