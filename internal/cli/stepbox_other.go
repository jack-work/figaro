//go:build !linux

package cli

import "errors"

type stepBox struct{}

func stepMarker(n uint64) []byte { return nil }

func resizeMarker(cols, rows int) []byte { return nil }

func openStepBox(cols, rows int) (*stepBox, error) {
	return nil, errors.New("--lockstep runs on linux only")
}

func (b *stepBox) setSize(cols, rows int) error { return nil }

func (b *stepBox) expect(marker []byte, reached func(total int64)) {}

func (b *stepBox) close() {}
