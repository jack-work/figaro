//go:build !linux

package cli

func isTmpfs(string) (bool, error) { return false, nil }
