//go:build !windows

package term

import "golang.org/x/sys/unix"

// MakeRaw puts fd into raw mode: per-keystroke input, no echo, and no signal
// generation (ISIG) or extended input processing (IEXTEN), so Ctrl-C (0x03) and Ctrl-D (0x04)
// arrive as ordinary input bytes rather than raising SIGINT/EOF. This matches
// the Windows VT-input model (Ctrl-C as byte, not signal) and keeps the
// interactive prompt loop portable across platforms.
func MakeRaw(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}

// ArmCookedInput is a no-op off Windows: a UNIX terminal is left cooked by the
// shell that hands it over, and readline resets it besides.
func ArmCookedInput() func() { return func() {} }

// SanitizeInput is a no-op off Windows. See the Windows half for why it must
// exist there.
func SanitizeInput() {}
