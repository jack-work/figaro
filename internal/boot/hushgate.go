package boot

import (
	"errors"
	"os"
	"strconv"
	"sync"
)

const (
	HushGateEnv       = "_FIGARO_HUSH_GATE"
	HushGateOpen byte = 'y'
)

type HushGate struct {
	f    *os.File
	once sync.Once
	open bool
}

var (
	daemonHushGate  *HushGate
	ErrHushGateShut = errors.New("the spawning client could not start the hush agent")
)

func TakeHushGate() *HushGate {
	v, ok := os.LookupEnv(HushGateEnv)
	os.Unsetenv(HushGateEnv)
	fd, err := strconv.Atoi(v)
	if !ok || err != nil || fd < 3 {
		daemonHushGate = nil
		return nil
	}
	closeOnExec(fd)
	daemonHushGate = &HushGate{f: os.NewFile(uintptr(fd), "hush-gate")}
	return daemonHushGate
}

func (g *HushGate) Await() bool {
	if g == nil {
		return true
	}
	g.once.Do(func() {
		defer g.f.Close()
		var b [1]byte
		n, _ := g.f.Read(b[:])
		g.open = n == 1 && b[0] == HushGateOpen
	})
	return g.open
}
