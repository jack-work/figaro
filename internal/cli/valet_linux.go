//go:build linux

package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/activation"
	"github.com/jack-work/figaro/internal/sandbox/valet"
)

func runValetServe(v valet.Valet) error {
	sockets, err := activation.TakeEnv()
	if err != nil {
		return err
	}
	nc, ok, err := sockets.Conn(activation.Accepted)
	if !ok && err == nil {
		if names := sockets.Names(); len(names) == 1 {
			nc, ok, err = sockets.Conn(names[0])
		}
	}
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("valet serve: no connection was handed over; run it from figaro-valet.socket with Accept=yes")
	}
	_ = sockets.Close()
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		nc.Close()
		return fmt.Errorf("valet serve: the handed-over connection is %T, not a unix socket", nc)
	}
	cmd, err := v.Answer(uc, time.Now())
	nc.Close()
	if err != nil {
		return err
	}
	if handed, err := valet.Notify(fmt.Sprintf("MAINPID=%d\nREADY=1", cmd.Process.Pid)); handed {
		return nil
	} else if err != nil {
		fmt.Fprintf(stderrw, "valet serve: notify: %v\n", err)
	}
	if err := cmd.Wait(); err != nil {
		if exit, ok := err.(interface{ ExitCode() int }); ok {
			exitAt(exit.ExitCode())
		}
		return err
	}
	return nil
}

func runValetFence() error { return valet.RunFence() }

func runValetFenced() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return valet.RunFenced(ctx)
}
