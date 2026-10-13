//go:build !windows

package term

func ArmOutput() func() { return func() {} }

func ArmDeferredWrap() func() { return func() {} }
