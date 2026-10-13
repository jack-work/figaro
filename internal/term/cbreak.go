//go:build !windows

package term

import "golang.org/x/sys/unix"

// MakeRaw: Ctrl-C and Ctrl-D arrive as bytes (ISIG off, matching the Windows
// VT-input model), and IXON is off so Ctrl-S reaches the program instead of
// stopping output.
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

func ArmCookedInput() func() { return func() {} }

func SanitizeInput() {}
