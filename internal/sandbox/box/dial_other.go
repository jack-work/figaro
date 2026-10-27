//go:build !linux

package box

import (
	"context"
	"fmt"
	"net"
	"os"
)

func dialSocket(ctx context.Context, d *net.Dialer, path string) (net.Conn, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("%s is not a socket", path)
	}
	return d.DialContext(ctx, "unix", path)
}
