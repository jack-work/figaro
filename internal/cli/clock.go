package cli

import (
	"sync"
	"time"
)

type clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
	Every(d time.Duration, f func()) (stop func())
}

var clk clock = wallClock{}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

func (wallClock) Every(d time.Duration, f func()) func() {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				f()
			}
		}
	}()
	return sync.OnceFunc(func() { close(stop) })
}
