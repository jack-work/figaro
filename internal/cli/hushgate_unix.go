//go:build !windows

package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/hush/managed"
)

type hushGate struct{ r, w *os.File }

func gateOnHush(cmd *exec.Cmd, _ *managed.Hush) (*hushGate, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, r)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", boot.HushGateEnv, 2+len(cmd.ExtraFiles)))
	return &hushGate{r: r, w: w}, nil
}

func (g *hushGate) open(h *managed.Hush) <-chan error {
	g.r.Close()
	ready := make(chan error, 1)
	go func() {
		err := h.EnsureReady()
		g.decide(err)
		ready <- err
	}()
	return ready
}

func (g *hushGate) decide(err error) {
	g.r.Close()
	if err == nil {
		g.w.Write([]byte{boot.HushGateOpen})
	}
	g.w.Close()
}
