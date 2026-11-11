//go:build !linux

package daemon

func userManagerPATH(string) (string, error) { return "", errNoUserManager }
