//go:build !linux

package cli

import (
	"errors"

	"github.com/jack-work/figaro/internal/sandbox/valet"
)

var errValetLinux = errors.New("valet: the valet serves only on Linux")

func runValetServe(valet.Valet) error { return errValetLinux }

func runValetFence() error { return errValetLinux }

func runValetFenced() error { return errValetLinux }
