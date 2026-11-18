//go:build !linux

package valet

import "errors"

func Available() error {
	return errors.New("valet: the fence is built from Linux namespaces; this system has none")
}
