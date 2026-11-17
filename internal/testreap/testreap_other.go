//go:build !linux

package testreap

import (
	"os"
	"time"
)

type proc struct {
	pid  int
	args string
}

type Owner struct{}

func Start() *Owner {
	owner = newOwner()
	if err := os.Setenv(OwnerEnv, owner); err != nil {
		panic(err)
	}
	return &Owner{}
}

func (o *Owner) Check(code int) int { return code }

func outliving(string, time.Duration, int) []proc { return nil }

func end(string, []proc, int) {}
