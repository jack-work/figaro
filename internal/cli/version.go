package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
)

func runVersion(asJSON bool) {
	v, ok := boot.Version()
	if !ok {
		fmt.Fprintln(os.Stdout, "figaro (build info unavailable)")
		return
	}
	v.Node = reachableNode()
	if asJSON {
		json.NewEncoder(os.Stdout).Encode(v)
		return
	}
	boot.PrintVersion(os.Stdout, v)
}

func reachableNode() string {
	cli, err := sdk.DialAngelus(doorEndpoint())
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
