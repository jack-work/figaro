//go:build !linux

package cli

import "errors"

func moveIntoScope(angelusPlacement, int) (string, error) {
	return "", errors.ErrUnsupported
}
