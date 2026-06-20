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
