//go:build windows

package boot

import (
	"errors"
	"os"
	"time"
)

func awaitAgentGone(runtimeDir string, within time.Duration) error {
	sock := HushAgentSocket(runtimeDir)
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(sock); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return nil
}
