//go:build windows

package boot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

func awaitAgentGone(ctx context.Context, runtimeDir string, within time.Duration) error {
	sock := HushAgentSocket(runtimeDir)
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(sock); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("hush: stopped waiting for the old agent's socket %s to go: %w", sock, ctx.Err())
		}
	}
	return nil
}
