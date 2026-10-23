package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
)

const modelsRefreshTimeout = 45 * time.Second

func runModels(loaded *config.Loaded, refresh, asJSON bool) error {
	var catalog form.Snapshot
	var err error
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		if refresh {
			ctx, cancel := context.WithTimeout(context.Background(), modelsRefreshTimeout)
			resp, rerr := acli.ModelsRefresh(ctx, true)
			cancel()
			switch {
			case rerr != nil:
				fmt.Fprintf(stderrw, "warning: refresh: %s\n", wireErrorText(rerr))
			case !resp.Done:
				fmt.Fprintln(stderrw, "warning: refresh: some providers are still listing; showing what has landed")
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		catalog, err = readCatalog(ctx, acli)
		return nil
	})
	if err != nil {
		return fmt.Errorf("models: %w", err)
	}
	listings := catalogListings(catalog, time.Now())
	if asJSON {
		if listings == nil {
			listings = []providerListing{}
		}
		return json.NewEncoder(stdout).Encode(listings)
	}
	printModels(listings, time.Now())
	return nil
}

func readCatalog(ctx context.Context, acli *sdk.Angelus) (form.Snapshot, error) {
	if _, err := acli.Attach(ctx, models.FormID); err != nil {
		return form.Snapshot{}, err
	}
	conn, err := sdk.DialAria(ariaAddr(models.FormID), nil)
	if err != nil {
		return form.Snapshot{}, err
	}
	defer conn.Close()
	resp, err := conn.Form(ctx)
	if err != nil {
		return form.Snapshot{}, err
	}
	return resp.Snapshot, nil
}

func printModels(listings []providerListing, now time.Time) {
	if len(listings) == 0 {
		fmt.Fprintln(stdout, "no models yet: the daemon lists every provider in the background (figaro models --refresh)")
		return
	}
	status := make([][]string, 0, len(listings))
	for _, l := range listings {
		status = append(status, []string{l.Provider, l.status(now)})
	}
	for _, line := range tabulate(status, []tableCol{{}, {}}, 0) {
		fmt.Fprintln(stdout, line)
	}
	cells := [][]string{{"PROVIDER", "MODEL", "NAME", "CONTEXT", "MAX OUT"}}
	for _, l := range listings {
		for _, m := range l.pickable() {
			cells = append(cells, modelCells(l.Provider, m))
		}
	}
	if len(cells) == 1 {
		return
	}
	fmt.Fprintln(stdout)
	for _, line := range tabulate(cells, modelCols, 0) {
		fmt.Fprintln(stdout, line)
	}
}
