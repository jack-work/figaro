package cli

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/tape"
)

const (
	lockstepSentinel = "tape.step"
	lockstepWait     = 10 * time.Second
	lockstepFPS      = 60
	lockstepAnimFPS  = 12
	lockstepMinCols  = 20
	lockstepMinRows  = 5
	lockstepMaxScale = 4
)

type lockstep struct {
	ctx    context.Context
	fps    int
	clk    *stepClock
	srv    *tapeServer
	box    *stepBox
	driver net.Conn
	end    chan struct{}
	cols   int
	rows   int

	acks     chan struct{}
	attached chan struct{}

	mu      sync.Mutex
	animFPS int
	session *lockstepSession
	tick    func()
	stopAni func()
}

type lockstepSession struct {
	mu     *renderLock
	lt     *livelogTurn
	resize func(w, h int)
}

var activeLockstep *lockstep

func acceptLockstepDriver(path string) (net.Conn, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		os.Remove(path)
	}
	ln, err := transport.Listen(transport.UnixEndpoint(path))
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	got := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			close(got)
			return
		}
		got <- conn
	}()
	select {
	case conn, ok := <-got:
		if !ok {
			return nil, fmt.Errorf("accept on %s failed", path)
		}
		return conn, nil
	case <-time.After(lockstepWait):
		return nil, fmt.Errorf("no driver connected to %s in %s", path, lockstepWait)
	}
}

func startLockstep(ctx context.Context, h tape.Header, srv *tapeServer, driver net.Conn, fps, animFPS int, started time.Time) (*lockstep, error) {
	cols, rows := h.Cols, h.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	box, err := openStepBox(cols, rows)
	if err != nil {
		return nil, err
	}
	ls := &lockstep{
		ctx: ctx, fps: fps, clk: newStepClock(started), srv: srv, box: box, driver: driver, cols: cols, rows: rows,
		end: make(chan struct{}), acks: make(chan struct{}, 1), attached: make(chan struct{}),
		animFPS: animFPS,
	}
	srv.stepped = ls.clk.elapsed
	srv.ready = make(chan struct{})
	clk = ls.clk
	activeLockstep = ls
	go ls.run()
	return ls, nil
}

func (ls *lockstep) close() {
	ls.driver.Close()
	ls.box.close()
}

func (ls *lockstep) attach(s *lockstepSession) {
	ls.mu.Lock()
	ls.session = s
	ls.mu.Unlock()
	close(ls.attached)
}

func (ls *lockstep) animate(tick func()) func() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.tick = tick
	ls.stopAni = ls.clk.Every(time.Second/time.Duration(ls.animFPS), tick)
	return func() {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		ls.stopAni()
		ls.tick = nil
	}
}

func (ls *lockstep) setRate(n int) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.animFPS = n
	if ls.tick != nil {
		ls.stopAni()
		ls.stopAni = ls.clk.Every(time.Second/time.Duration(n), ls.tick)
	}
}

func (ls *lockstep) ack() {
	select {
	case ls.acks <- struct{}{}:
	default:
	}
}

func (ls *lockstep) stepAt(n uint64) time.Duration {
	return time.Duration(int64(n) * int64(time.Second) / int64(ls.fps))
}

func (ls *lockstep) run() {
	defer close(ls.end)
	r := bufio.NewReader(ls.driver)
	var next uint64
	for {
		line, err := r.ReadString('\n')
		if words := strings.Fields(line); len(words) > 0 && !ls.command(words, &next) {
			return
		}
		if err != nil {
			return
		}
	}
}

func (ls *lockstep) command(words []string, next *uint64) bool {
	switch {
	case words[0] == "rate" && len(words) == 2:
		n, err := strconv.Atoi(words[1])
		if err != nil || n <= 0 {
			return ls.reply("error rate %q: want a positive number of frames per second", words[1])
		}
		ls.setRate(n)
		return true
	case words[0] == "step" && len(words) <= 2:
		target := *next
		if len(words) == 2 {
			n, err := strconv.ParseUint(words[1], 10, 64)
			if err != nil {
				return ls.reply("error step %q: not a step number", words[1])
			}
			if n < *next {
				return ls.reply("error step %d is behind %d", n, *next)
			}
			target = n
		}
		for ; *next <= target; *next++ {
			total, ok := ls.step(*next)
			if !ok || !ls.reply("done %d %d", *next, total) {
				return false
			}
		}
		return true
	case words[0] == "resize" && len(words) == 3:
		cols, rows, err := ls.resizeBounds(words[1], words[2])
		if err != nil {
			return ls.reply("error resize %s %s: %s", words[1], words[2], err)
		}
		total, ok := ls.resize(cols, rows)
		return ok && ls.reply("resized %d %d %d", cols, rows, total)
	}
	return ls.reply("error unknown command %q", strings.Join(words, " "))
}

func (ls *lockstep) resizeBounds(colArg, rowArg string) (int, int, error) {
	cols, err := strconv.Atoi(colArg)
	if err != nil {
		return 0, 0, fmt.Errorf("columns not a number")
	}
	rows, err := strconv.Atoi(rowArg)
	if err != nil {
		return 0, 0, fmt.Errorf("rows not a number")
	}
	maxCols, maxRows := ls.cols*lockstepMaxScale, ls.rows*lockstepMaxScale
	if cols < lockstepMinCols || rows < lockstepMinRows || cols > maxCols || rows > maxRows {
		return 0, 0, fmt.Errorf("want %d to %d columns and %d to %d rows", lockstepMinCols, maxCols, lockstepMinRows, maxRows)
	}
	return cols, rows, nil
}

func (ls *lockstep) reply(format string, args ...any) bool {
	_, err := fmt.Fprintf(ls.driver, format+"\n", args...)
	return err == nil
}

func (ls *lockstep) started() bool {
	for _, ready := range []chan struct{}{ls.srv.ready, ls.attached} {
		select {
		case <-ready:
		case <-ls.ctx.Done():
			return false
		}
	}
	return true
}

func (ls *lockstep) step(n uint64) (int64, bool) {
	if !ls.started() {
		return 0, false
	}
	at := ls.stepAt(n)
	for {
		settling.wait()
		frame, haveFrame := ls.srv.nextAt()
		due, haveTimer := ls.clk.nextDue()
		frameDue := haveFrame && frame <= at
		timerDue := haveTimer && due <= at
		if !frameDue && !timerDue {
			break
		}
		if frameDue && (!timerDue || frame <= due) {
			bound := at
			if timerDue {
				bound = due
			}
			if !ls.deliver(bound) {
				return 0, false
			}
			continue
		}
		ls.clk.fireOne(at)
	}
	ls.clk.advance(at)
	return ls.flushThrough(stepMarker(n))
}

func (ls *lockstep) resize(cols, rows int) (int64, bool) {
	if !ls.started() || ls.box.setSize(cols, rows) != nil {
		return 0, false
	}
	ls.session.resize(cols, rows)
	return ls.flushThrough(resizeMarker(cols, rows))
}

func (ls *lockstep) flushThrough(marker []byte) (int64, bool) {
	settling.wait()
	s := ls.session
	s.mu.Lock()
	s.lt.lockstepFlush()
	s.mu.Unlock()
	reached := make(chan int64, 1)
	ls.box.expect(marker, func(total int64) { reached <- total })
	if _, err := os.Stdout.Write(marker); err != nil {
		return 0, false
	}
	select {
	case total := <-reached:
		return total, true
	case <-ls.ctx.Done():
		return 0, false
	}
}

func (ls *lockstep) deliver(bound time.Duration) bool {
	if !ls.srv.pushThrough(bound, ls.clk.advance) {
		return false
	}
	if ls.srv.main.Notify(lockstepSentinel, struct{}{}) != nil {
		return false
	}
	select {
	case <-ls.acks:
		return true
	case <-ls.ctx.Done():
		return false
	}
}

func lockstepReplayArgs(args []string) bool {
	if len(args) == 0 || args[0] != "replay" {
		return false
	}
	for _, a := range args[1:] {
		if a == "--" {
			return false
		}
		if a == "--lockstep" || strings.HasPrefix(a, "--lockstep=") {
			return true
		}
	}
	return false
}
