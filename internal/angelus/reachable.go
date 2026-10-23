package angelus

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"time"
)

const reachableEvery = 2 * time.Second

func writePidFile(path string) {
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
}

func (a *Angelus) keepReachable(ctx context.Context, pidPath string) {
	t := time.NewTicker(reachableEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if a.Registry != nil && a.Registry.IsDraining() {
			return
		}
		a.restoreReachability(ctx, pidPath)
	}
}

func (a *Angelus) restoreReachability(ctx context.Context, pidPath string) {
	if _, err := os.Stat(a.SocketPath); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(a.RuntimeDir, 0o700); err != nil {
			slog.Warn("socket removed; runtime dir cannot be recreated", "dir", a.RuntimeDir, "err", err)
			return
		}
		ln, err := net.Listen("unix", a.SocketPath)
		if err != nil {
			slog.Warn("socket removed; cannot listen again", "socket", a.SocketPath, "err", err)
			return
		}
		if err := os.Chmod(a.SocketPath, 0600); err != nil {
			ln.Close()
			slog.Warn("socket removed; cannot restrict the new one", "socket", a.SocketPath, "err", err)
			return
		}
		slog.Warn("socket was removed under the running daemon; listening again", "socket", a.SocketPath)
		go func() { _ = a.Serve(ctx, ln, a.cliAdmit()) }()
	}
	if _, err := os.Stat(pidPath); errors.Is(err, fs.ErrNotExist) {
		writePidFile(pidPath)
		slog.Warn("pid file was removed under the running daemon; rewritten", "path", pidPath)
	}
}
