package term

import "golang.org/x/sys/unix"

// MakeCbreak puts fd into cbreak mode: per-keystroke input with no echo,
// signals still delivered (so Ctrl-C still raises SIGINT) and output
// post-processing preserved (so "\n" still maps to CRLF — the live painter
// relies on that). Returns a restore func; call it (deferred) to undo.
func MakeCbreak(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ICANON | unix.ECHO
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}

// MakeRaw is like MakeCbreak but also disables signal generation (ISIG) and
// extended input processing (IEXTEN), so Ctrl-C (0x03) and Ctrl-D (0x04)
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
