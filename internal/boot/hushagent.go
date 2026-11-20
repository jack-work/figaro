package boot

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/jack-work/hush/managed"
)

var hushAgentMu sync.RWMutex

func ReviveHushAgent(h *managed.Hush) error {
	hushAgentMu.Lock()
	defer hushAgentMu.Unlock()
	return h.EnsureReady()
}

func RenewHushAgent(ctx context.Context, h *managed.Hush, margin time.Duration) (renewed bool, err error) {
	hushAgentMu.Lock()
	defer hushAgentMu.Unlock()
	left, err := h.Client().Status()
	if err != nil {
		return false, h.EnsureReady()
	}
	if h.Mode() != managed.ModeEmbedded {
		return false, nil
	}
	remaining, err := time.ParseDuration(left)
	if err != nil || remaining > margin {
		return false, nil
	}
	if err := h.Client().Shutdown(); err != nil && !dialRefused(err) {
		return false, err
	}
	if err := awaitAgentGone(ctx, h.Config().RuntimeDir, hushAgentDrainCeiling); err != nil {
		return false, err
	}
	return true, h.EnsureReady()
}

const hushAgentDrainCeiling = 2 * time.Minute

func dialRefused(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func hushAgentGone(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" || !dialRefused(err) {
		return false
	}
	addr, ok := op.Addr.(*net.UnixAddr)
	if !ok {
		return false
	}
	h := usedHush()
	return h != nil && h.Config() != nil && addr.Name == HushAgentSocket(h.Config().RuntimeDir)
}

func usedHush() *managed.Hush {
	hushUsersMu.Lock()
	defer hushUsersMu.Unlock()
	return hushUsed
}

func reviveUsedHush() error {
	h := usedHush()
	if h == nil {
		return errors.New("hush: no agent in use")
	}
	return ReviveHushAgent(h)
}
