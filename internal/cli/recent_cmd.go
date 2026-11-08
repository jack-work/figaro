package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/recent"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk"
)

func runRecent(loaded *config.Loaded, limit int, asJSON bool) {
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := acli.Form(ctx, rpc.RecentFormID)
		if err != nil {
			die("recent: %s", err)
		}
		rows := recent.Decode(resp.Snapshot)
		if limit > 0 && len(rows) > limit {
			rows = rows[:limit]
		}
		if asJSON {
			if rows == nil {
				rows = []recent.Row{}
			}
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(rows)
		}
		width := 0
		if term.IsTerminal(int(os.Stdout.Fd())) {
			width = term.Width()
		}
		printRecent(stdout, rows, width)
		return nil
	})
}

func printRecent(w io.Writer, rows []recent.Row, width int) {
	nameW, titleW, ageW := 0, 0, 0
	ages := make([]string, len(rows))
	for i, r := range rows {
		nameW = max(nameW, min(displayWidth(r.Name), labelPadMax))
		titleW = max(titleW, displayWidth(r.Title))
		ages[i] = relAge(r.LastActive)
		ageW = max(ageW, len(ages[i]))
	}
	sink, rowed := w.(rowSink)
	for i, r := range rows {
		line := r.Name + pad(nameW-displayWidth(r.Name)) + "  " + r.Title
		age := pad(ageW-len(ages[i])) + ages[i]
		if rowed {
			sink.WriteRow(line, listedAria{id: r.ID, name: r.Name, aside: ages[i]})
			continue
		}
		if width > 0 {
			line = withAside(line, age, width)
		} else {
			line += pad(titleW-displayWidth(r.Title)) + "  " + age
		}
		fmt.Fprintln(w, line)
	}
}

func pad(n int) string { return strings.Repeat(" ", max(n, 0)) }
