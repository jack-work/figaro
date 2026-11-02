//go:build !linux

package upstream

func watchRoutes(func()) error { return nil }
