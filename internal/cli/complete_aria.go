package cli

import (
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

func completeAriaIDsAfterFlag(inner func(*cmdkit.CompleteContext) []string) func(*cmdkit.CompleteContext) []string {
	return func(c *cmdkit.CompleteContext) []string {
		if c != nil && len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
			return ariaCandidates(c)
		}
		if inner != nil {
			return inner(c)
		}
		return nil
	}
}

func completeAriaIDsPositionalOrFlag(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return ariaCandidates(c)
	}
	// The first positional slot is empty when only flags precede the cursor; a
	// value-taking flag's value reads as a positional, erring toward offering
	// nothing.
	for _, a := range c.Args {
		if !strings.HasPrefix(a, "-") {
			return nil
		}
	}
	return ariaCandidates(c)
}
