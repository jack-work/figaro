package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/sandbox/box"
)

var boxVerb = &cmdkit.Command{
	Name:  "box",
	Group: "System",
	Short: "Serve a sandbox over a unix socket, or run one command through it",
	Usage: "box serve --socket <path> --workspace <dir> | box exec --socket <path> [--cwd <dir>] [--scope <s>] -- <command>",
	Long: `A box is a sandbox a daemon reaches over a unix socket: the daemon names
it in [sandbox.boxes.<name>] and an aria picks it with system.sandbox.

  serve   run the local provider behind --socket, rooted at --workspace.
          It holds no figaro state and needs no daemon or config. The
          socket takes the process umask; a stale socket is replaced, a
          live one refused. SIGINT or SIGTERM kills every session it holds
  exec    run one shell command through a served box, stream its output
          and exit with its code (130 cancelled, 124 timed out). stdin
          is not forwarded`,
	Flags: []cmdkit.FlagDef{
		{Long: "socket", Short: "s", Description: "the box's unix socket"},
		{Long: "workspace", Short: "w", Description: "serve: the directory every relative path and cwd resolves against"},
		{Long: "cwd", Description: "exec: working directory, relative to the workspace"},
		{Long: "scope", Description: "exec: session scope (default: this pid)"},
	},
	ArgsMin: 1,
	Run: func(ctx *cmdkit.RunContext) error {
		sock := ctx.Flag("socket")
		if sock == "" {
			return fmt.Errorf("box: --socket is required")
		}
		switch ctx.Args[0] {
		case "serve":
			if len(ctx.Args) > 1 {
				return fmt.Errorf("box serve takes no arguments")
			}
			return runBoxServe(sock, ctx.Flag("workspace"))
		case "exec":
			if len(ctx.Args) < 2 {
				return fmt.Errorf("box exec: name a command after --")
			}
			code, err := runBoxExec(sock, ctx.Flag("cwd"), ctx.Flag("scope"), strings.Join(ctx.Args[1:], " "))
			if err != nil {
				return err
			}
			exitAt(code)
			return nil
		}
		return fmt.Errorf("box: want serve or exec, not %q", ctx.Args[0])
	},
}

func runBoxStandalone(progName string, args []string) int {
	r := cmdkit.NewRouter(progName)
	r.Register(boxVerb)
	return r.Run(args)
}

func runBoxServe(sock, workspace string) error {
	if workspace == "" {
		return fmt.Errorf("box serve: --workspace is required")
	}
	srv, err := box.NewServer(workspace)
	if err != nil {
		return err
	}
	ln, err := box.Listen(sock)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stderrw, "box: serving %s on %s\n", workspace, sock)
	return srv.Serve(ctx, ln)
}

func runBoxExec(sock, cwd, scope, command string) (int, error) {
	if scope == "" {
		scope = fmt.Sprintf("exec-%d", os.Getpid())
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	b, err := box.Provider{Name: sock, Socket: sock, Scope: scope}.Open(ctx, sandbox.Spec{})
	if err != nil {
		return 0, err
	}
	defer b.Close()
	p, err := b.Exec(ctx, sandbox.ExecRequest{Command: command, Cwd: cwd, OnChunk: func(c []byte) { os.Stdout.Write(c) }})
	if err != nil {
		return 0, err
	}
	res := p.Wait()
	switch {
	case res.Canceled:
		return 130, nil
	case res.TimedOut:
		return 124, nil
	}
	return res.ExitCode, nil
}
