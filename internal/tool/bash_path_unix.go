//go:build !windows

package tool

// bashPath returns "bash" on Unix (resolved via PATH as usual).
func bashPath() string { return "bash" }
