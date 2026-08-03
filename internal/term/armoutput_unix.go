//go:build !windows

package term

// ArmOutput is a no-op off Windows: a UNIX terminal honours escapes without
// being asked, and stdout carries no mode to save. It exists so the CLI can
// arm unconditionally at startup rather than branching per platform.
func ArmOutput() func() { return func() {} }

// ArmDeferredWrap is a no-op off Windows: a UNIX terminal already defers the
// right-edge wrap, and LF there never stopped implying a carriage return.
func ArmDeferredWrap() func() { return func() {} }
