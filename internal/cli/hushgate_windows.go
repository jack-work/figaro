//go:build windows

package cli

import (
	"os/exec"

	"github.com/jack-work/hush/managed"
)

type hushGate struct{}

func gateOnHush(_ *exec.Cmd, h *managed.Hush) (*hushGate, error) {
	if err := h.EnsureReady(); err != nil {
		dieHush(err)
	}
	return &hushGate{}, nil
}

func (g *hushGate) open(*managed.Hush) <-chan error {
	ready := make(chan error, 1)
	ready <- nil
	return ready
}

func (g *hushGate) decide(error) {}
