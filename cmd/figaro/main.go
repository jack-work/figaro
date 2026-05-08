// figaro is a minimal CLI coding agent.
//
// Usage:
//
//	figaro -- <prompt>               # shorthand for `figaro qua -- <prompt>`
//	figaro qua -- <prompt>           # prompt the shell-bound figaro (or create one)
//	figaro qua <id> -- <prompt>      # one-shot prompt to an arbitrary aria
//	figaro new -- <prompt>           # new figaro + prompt
//	figaro context                   # show chat history
//	figaro list                      # list all figaros
//	figaro kill <id>                 # kill a figaro
//	figaro models                    # list available models
//	figaro login <provider>          # OAuth login
//	figaro --angelus                 # (internal) run as supervisor
//
// All CLI behavior lives in internal/cli. This file is the binary
// entrypoint plus the hush re-exec guard.
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
