package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/sandbox/box"
)

var boxVerb = &cmdkit.Command{
	Name:  "box",
	Group: "System",
	Short: "Serve a sandbox over a unix socket",
	Usage: "box serve --socket <path> --workspace <dir>",
	Long: `A box is a sandbox a daemon reaches over a unix socket: the daemon names
it in [sandbox.boxes.<name>] and an aria picks it with system.sandbox.

  serve   run the local provider behind --socket, rooted at --workspace.
          It holds no figaro state and needs no daemon or config. The
          socket takes the process umask; a stale socket is replaced, a
          live one refused. SIGINT or SIGTERM kills every session it holds`,
	Flags: []cmdkit.FlagDef{
		{Long: "socket", Short: "s", Description: "the box's unix socket"},
		{Long: "workspace", Short: "w", Description: "serve: the directory every relative path and cwd resolves against"},
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
		}
		return fmt.Errorf("box: want serve, not %q", ctx.Args[0])
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
