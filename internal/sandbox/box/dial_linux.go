package box

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

func dialSocket(ctx context.Context, d *net.Dialer, path string) (net.Conn, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, fmt.Errorf("%s is not a socket", path)
	}
	return d.DialContext(ctx, "unix", "/proc/self/fd/"+strconv.Itoa(fd))
}
