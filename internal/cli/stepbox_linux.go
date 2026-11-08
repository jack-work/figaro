package cli

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	xterm "golang.org/x/term"
)

const stepBoxDrain = 200 * time.Millisecond

type stepBox struct {
	master *os.File
	slave  *os.File
	out    *os.File
	inFD   int
	outFD  int
	unraw  func()
	copied chan struct{}
	closed sync.Once

	mu    sync.Mutex
	want  []byte
	total int64
	mark  func(total int64)
}

func stepMarker(n uint64) []byte { return fmt.Appendf(nil, "\x1b]7717;step=%d\x07", n) }

func openStepBox(cols, rows int) (*stepBox, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, err
	}
	if err := pty.Setsize(slave, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	inFD, err := unix.Dup(0)
	if err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	outFD, err := unix.Dup(1)
	if err != nil {
		unix.Close(inFD)
		master.Close()
		slave.Close()
		return nil, err
	}
	b := &stepBox{master: master, slave: slave, out: os.NewFile(uintptr(outFD), "stdout"),
		inFD: inFD, outFD: outFD, unraw: func() {}, copied: make(chan struct{})}
	go b.copy()
	if xterm.IsTerminal(outFD) {
		if st, err := xterm.MakeRaw(outFD); err == nil {
			b.unraw = func() { _ = xterm.Restore(outFD, st) }
		}
	}
	for _, fd := range []int{0, 1} {
		if err := unix.Dup3(int(slave.Fd()), fd, 0); err != nil {
			b.close()
			return nil, err
		}
	}
	return b, nil
}

func (b *stepBox) expect(marker []byte, reached func(total int64)) {
	b.mu.Lock()
	b.want, b.mark = marker, reached
	b.mu.Unlock()
}

func (b *stepBox) copy() {
	defer close(b.copied)
	buf := make([]byte, 64<<10)
	var tail []byte
	for {
		n, err := b.master.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := b.out.Write(chunk); werr != nil {
				return
			}
			b.mu.Lock()
			before := b.total
			b.total += int64(n)
			want, mark := b.want, b.mark
			var reached int64 = -1
			if want != nil {
				window := append(tail, chunk...)
				if i := bytes.Index(window, want); i >= 0 {
					reached = before - int64(len(tail)) + int64(i+len(want))
					b.want, b.mark = nil, nil
					tail = tail[:0]
				} else {
					if keep := len(want) - 1; len(window) > keep {
						window = window[len(window)-keep:]
					}
					tail = append(tail[:0], window...)
				}
			}
			b.mu.Unlock()
			if reached >= 0 {
				mark(reached)
			}
		}
		if err != nil {
			return
		}
	}
}

func (b *stepBox) close() {
	b.closed.Do(func() {
		_ = unix.Dup3(b.inFD, 0, 0)
		_ = unix.Dup3(b.outFD, 1, 0)
		b.slave.Close()
		_ = b.master.SetReadDeadline(time.Now().Add(stepBoxDrain))
		<-b.copied
		b.unraw()
		b.master.Close()
		unix.Close(b.inFD)
		b.out.Close()
	})
}
