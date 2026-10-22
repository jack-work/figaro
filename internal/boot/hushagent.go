package boot

import (
	"errors"
	"net"
	"sync"
	"syscall"

	"github.com/jack-work/hush/managed"
)

var hushAgentMu sync.Mutex

func ReviveHushAgent(h *managed.Hush) error {
	hushAgentMu.Lock()
	defer hushAgentMu.Unlock()
	return h.EnsureReady()
}

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
