// figaro is a minimal CLI coding agent.
//
// Run `figaro --help` for the full command listing. All CLI behavior
// lives in internal/cli (routed via internal/cmdkit). This file is
// the binary entrypoint plus the hush re-exec guard.
package main

import (
	"log"
	"os"

	"github.com/jack-work/hush/managed"

	"github.com/jack-work/figaro/internal/cli"
)

func main() {
	// Re-exec guard: if we were spawned by managed.SpawnDaemon to serve
	// as the embedded hush agent, run it and exit immediately.
	if managed.IsAgentChild() {
		if err := managed.RunAgentChild(); err != nil {
			log.Fatal(err)
		}
		return
	}

	cli.Run(os.Args[1:])
}
