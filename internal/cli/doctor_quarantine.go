package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
	"golang.org/x/term"
)

type quarantineEndpoint interface {
	QuarantineList(ctx context.Context) (*rpc.QuarantineListResponse, error)
	QuarantineRelease(ctx context.Context, req rpc.QuarantineReleaseRequest) (*rpc.QuarantineReleaseResponse, error)
}

type quarantineOpts struct {
	JSON        bool
	Yes         bool
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

func runDoctorQuarantine(args []string, at, channel string, asJSON, yes bool) error {
	cli, err := sdk.DialAngelus(daemonEndpoint())
	if err != nil {
		return fmt.Errorf("no angelus running: %w", err)
	}
	defer cli.Close()
	o := quarantineOpts{JSON: asJSON, Yes: yes, In: os.Stdin, Out: stdout,
		Interactive: term.IsTerminal(int(os.Stdin.Fd()))}
	return doctorQuarantine(context.Background(), cli, args, at, channel, o)
}

func doctorQuarantine(ctx context.Context, d quarantineEndpoint, args []string, at, channel string, o quarantineOpts) error {
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "ls"):
		return quarantineList(ctx, d, o)
	case len(args) == 2 && args[0] == "release":
		offset, err := strconv.ParseInt(at, 10, 64)
		if at == "" || err != nil || offset < 0 {
			return errors.New("quarantine release needs --at <byte offset>, as `figaro doctor quarantine ls` prints it")
		}
		return quarantineRelease(ctx, d, args[1], channel, offset, o)
	}
	return errors.New("usage: doctor quarantine [ls] | doctor quarantine release <figaro> --at <offset> [--channel <name>] [--yes]")
}

func quarantineList(ctx context.Context, d quarantineEndpoint, o quarantineOpts) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := d.QuarantineList(ctx)
	if err != nil {
		return err
	}
	if o.JSON {
		return json.NewEncoder(o.Out).Encode(resp.Entries)
	}
	if len(resp.Entries) == 0 {
		fmt.Fprintln(o.Out, "no segment holds a corrupt frame with valid frames after it")
		return nil
	}
	for _, e := range resp.Entries {
		fmt.Fprintf(o.Out, "%s  %s  byte %d  %s\n", e.Figaro, e.Channel, e.Offset, e.Segment)
		if len(e.Shared) > 0 {
			fmt.Fprintf(o.Out, "  in node %s, shared with %s: not releasable here\n", e.Node, strings.Join(e.Shared, ", "))
		}
	}
	return nil
}

func quarantineRelease(ctx context.Context, d quarantineEndpoint, figaro, channel string, offset int64, o quarantineOpts) error {
	req := rpc.QuarantineReleaseRequest{Figaro: figaro, Channel: channel, At: offset}
	plan, err := d.QuarantineRelease(ctx, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "%s %s: cut %s at byte %d, dropping %d bytes", plan.Figaro, plan.Channel, plan.Segment, plan.Offset, plan.BytesCut)
	if n := len(plan.LaterSegments); n > 0 {
		fmt.Fprintf(o.Out, " and %d later segment(s)", n)
	}
	fmt.Fprintf(o.Out, "\nthe records after the corrupt frame go with it; the original bytes are copied first to %s\n", plan.Aside)
	if !o.Yes {
		if !o.Interactive {
			return errors.New("not released: pass --yes to release without a terminal")
		}
		fmt.Fprintf(o.Out, "Type the offset (%d) to release: ", plan.Offset)
		line, _ := bufio.NewReader(o.In).ReadString('\n')
		if strings.TrimSpace(line) != strconv.FormatInt(plan.Offset, 10) {
			return errors.New("not released")
		}
	}
	req.Channel = plan.Channel
	req.Confirm = true
	done, err := d.QuarantineRelease(ctx, req)
	if err != nil {
		return err
	}
	if o.JSON {
		return json.NewEncoder(o.Out).Encode(done)
	}
	fmt.Fprintf(o.Out, "released: %s cut at byte %d; the original is in %s\n", done.Segment, done.Offset, done.Aside)
	for ch, n := range done.Trimmed {
		fmt.Fprintf(o.Out, "  %s: %d record(s) past the shortened history trimmed\n", ch, n)
	}
	if done.Warning != "" {
		fmt.Fprintf(o.Out, "warning: %s\n", done.Warning)
	}
	return nil
}
