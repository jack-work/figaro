package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/relay"
)

func runServe(loaded *config.Loaded, addr, only string) error {
	if addr == "" {
		addr = "127.0.0.1:8788"
	}
	var names []string
	for _, n := range strings.Split(only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}

	ln, err := relay.ListenLoopback(addr)
	if err != nil {
		return err
	}
	ups, skipped := relay.Upstreams(loaded, names)
	served := slices.Sorted(maps.Keys(ups))
	for _, n := range slices.Sorted(maps.Keys(skipped)) {
		fmt.Fprintf(stderrw, "  %s %-10s %s\n", dim("–"), n, dim(skipped[n]))
	}
	if len(ups) == 0 {
		ln.Close()
		return fmt.Errorf("serve: no provider has a usable credential")
	}

	log := slog.New(slog.NewTextHandler(stderrw, nil))
	srv := &http.Server{
		Handler:           relay.New(ups, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	base := "http://" + ln.Addr().String()
	for _, n := range served {
		fmt.Fprintf(stderrw, "  %s %-10s %s/u/%s\n", green("✓"), n, base, n)
	}
	fmt.Fprintf(stderrw, "  serving %s/v1 with models named <provider>/<model>\n", base)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
