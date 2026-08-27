//go:build !windows

package term

import "golang.org/x/sys/unix"

// MakeRaw puts fd into raw mode: per-keystroke input, no echo, and no signal
// generation (ISIG) or extended input processing (IEXTEN), so Ctrl-C (0x03) and Ctrl-D (0x04)
// arrive as ordinary input bytes rather than raising SIGINT/EOF. This matches
// the Windows VT-input model (Ctrl-C as byte, not signal) and keeps the
// interactive prompt loop portable across platforms.
//
// IT ALSO TURNS OFF SOFTWARE FLOW CONTROL (IXON), which is an input-flag bit
// and so was missed by the Lflag mask above. With it on, Ctrl-S never reaches
// this program: the tty driver eats it and STOPS THE OUTPUT, so a reader who
// presses it gets a figaro that looks hung until they happen to press Ctrl-Q.
// That is a trap on its own terms, and it is also what would make the ':' box's
// forward history search (^S, readline's) a dead key. Restored with the rest of
// the termios on the way out.
func MakeRaw(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	t.Iflag &^= unix.IXON
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
