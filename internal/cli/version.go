package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func runVersion(asJSON bool) {
	v, ok := boot.Version()
	if !ok {
		fmt.Fprintln(stdout, "figaro (build info unavailable)")
		return
	}
	v.Node = reachableNode()
	if asJSON {
		json.NewEncoder(stdout).Encode(v)
		return
	}
	boot.PrintVersion(stdout, v)
}

func reachableNode() string {
	cli, err := sdk.DialAngelus(daemonEndpoint())
	if err != nil {
		return ""
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil || st == nil {
		return ""
	}
	return st.Node
}
