//go:build !linux

package cli

import "errors"

func socketOwnerPID(string) (int, error) {
	return 0, errors.New("socket owner pid: linux only")
}
