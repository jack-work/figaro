//go:build !linux

package store

import (
	"errors"
	"os"
)

func cloneFile(out, in *os.File) error { return errors.ErrUnsupported }
