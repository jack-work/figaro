package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
)

// runHup sends figaro.interrupt to a trunk — the same RPC Ctrl-C
// in a send stream fires. With no id, the pid-bound aria is used.
func runHup(loaded *config.Loaded, ariaID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resolvedID, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false)
	if err != nil {
		die("%s", err)
	}

	fcli, derr := figaro.DialClient(ep, func(string, json.RawMessage) {})
	if derr != nil {
		die("connect figaro: %s", derr)
	}
	defer fcli.Close()

	if err := fcli.Interrupt(ctx); err != nil {
		die("hup %s: %s", resolvedID, err)
	}
	fmt.Printf("hup %s\n", resolvedID)
}
