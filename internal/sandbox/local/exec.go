package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/jack-work/figaro/api/sandbox"
)

const waitDelay = 3 * time.Second

var ptyStart = pty.Start

var (
	dsrQuery  = []byte("\x1b[6n")
	dsrReport = []byte("\x1b[1;1R")
)

type process struct {
	cmd   *exec.Cmd
	stdin io.Writer
	pty   bool
	done  chan struct{}
	res   sandbox.Result
}

func Start(ctx context.Context, argv []string, dir string, env []string, req sandbox.ExecRequest) (sandbox.Process, error) {
	cancel := context.CancelFunc(func() {})
	if req.Timeout > 0 {
		ctx, cancel = context.WithTimeoutCause(ctx, req.Timeout, errTimeout)
	}
	emit := func(b []byte) {
		if req.OnChunk != nil {
			req.OnChunk(b)
		}
	}
	mk := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, waitDelay
		cmd.Cancel = func() error { killTree(cmd); return nil }
		return cmd
	}
	p := &process{done: make(chan struct{})}
	var ptmx *os.File
	if req.PTY {
		cmd := mk()
		f, err := ptyStart(cmd)
		if err != nil {
			emit([]byte(fmt.Sprintf("PTY spawn failed, fell back to pipe: %v\n", err)))
		} else {
			p.cmd, p.stdin, p.pty, ptmx = cmd, f, true, f
		}
	}
	if p.cmd == nil {
		cmd := mk()
		cmd.SysProcAttr = procAttr()
		stdin, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			return nil, err
		}
		w := chunkWriter(emit)
		cmd.Stdout, cmd.Stderr = w, w
		if err := cmd.Start(); err != nil {
			cancel()
			return nil, fmt.Errorf("start command: %w", err)
		}
		p.cmd, p.stdin = cmd, stdin
	}
	afterStart(p.cmd)
	copied := make(chan struct{})
	if ptmx != nil {
		go func() {
			defer close(copied)
			buf := make([]byte, 32*1024)
			for {
				n, err := ptmx.Read(buf)
				if n > 0 {
					if bytes.Contains(buf[:n], dsrQuery) {
						ptmx.Write(dsrReport)
					}
					emit(bytes.Clone(buf[:n]))
				}
				if err != nil {
					return
				}
			}
		}()
	} else {
		close(copied)
	}
	go func() {
		p.cmd.Wait()
		afterWait(p.cmd)
		if ptmx != nil {
			ptmx.Close()
		}
		<-copied
		p.res.ExitCode = p.cmd.ProcessState.ExitCode()
		p.res.TimedOut = errors.Is(context.Cause(ctx), errTimeout)
		p.res.Canceled = ctx.Err() != nil && !p.res.TimedOut
		cancel()
		close(p.done)
	}()
	return p, nil
}

var errTimeout = errors.New("timeout")

func (p *process) Wait() sandbox.Result        { <-p.done; return p.res }
func (p *process) Done() <-chan struct{}       { return p.done }
func (p *process) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *process) Interrupt() error            { return signalTree(p.cmd, syscall.SIGINT) }
func (p *process) Kill() error                 { killTree(p.cmd); return nil }
func (p *process) PTY() bool                   { return p.pty }

type chunkWriter func([]byte)

func (w chunkWriter) Write(b []byte) (int, error) { w(bytes.Clone(b)); return len(b), nil }
