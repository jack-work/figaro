// figaro is a minimal CLI coding agent.
package main

import (
	"log"
	"os"

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

	cli.Run(os.Args[1:])
}
