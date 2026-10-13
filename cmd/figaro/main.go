package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/internal/cli"
	"github.com/jack-work/figaro/internal/daemon"
)

func main() {
	if managed.IsAgentChild() {
		if err := managed.RunAgentChild(); err != nil {
			log.Fatal(err)
		}
		return
	}

	// argv[0] carries the symlink name (fig, figarod) the user typed.
	name := filepath.Base(os.Args[0])
	if name == "figarod" || os.Getenv("_FIGARO_DAEMON") == "1" || (len(os.Args) > 1 && os.Args[1] == "--angelus") {
		daemon.Run()
		return
	}
	cli.Run(name, os.Args[1:])
}
