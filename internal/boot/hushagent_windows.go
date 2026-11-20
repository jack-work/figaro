//go:build windows

package boot

import (
	"context"
	"errors"
	"os"
	"time"
)

func awaitAgentGone(ctx context.Context, runtimeDir string, within time.Duration) error {
	sock := HushAgentSocket(runtimeDir)
	for deadline := time.Now().Add(within); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(sock); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	return nil
}
