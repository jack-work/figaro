// figaro is a minimal CLI coding agent.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/internal/cli"
)

func main() {
	// Re-exec guard for embedded hush agent.
	if managed.IsAgentChild() {
		if err := managed.RunAgentChild(); err != nil {
			log.Fatal(err)
		}
		return
	}

	// argv[0] basename is the invocation name. Symlinks like `fig`
	// (see flake.nix postInstall) flow through here so help/usage/
	// completion output the name the user actually typed.
	cli.Run(filepath.Base(os.Args[0]), os.Args[1:])
}
