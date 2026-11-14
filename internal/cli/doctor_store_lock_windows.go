package cli

import "fmt"

func takeInheritedStoreLock(string) error {
	return fmt.Errorf("%s is not supported on Windows", storeLockFDEnv)
}
