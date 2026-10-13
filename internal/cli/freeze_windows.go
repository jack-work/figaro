//go:build windows

package cli

func armFreezeSignals() func() { return func() {} }
