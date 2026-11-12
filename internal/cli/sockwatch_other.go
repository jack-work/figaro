//go:build !linux

package cli

func watchSocketCreated(string) (<-chan struct{}, func()) { return nil, func() {} }
