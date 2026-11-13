package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

func runDoctorResidents(loaded *config.Loaded, jsonOut bool) error {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	resp, err := acli.Residents(context.Background())
	if err != nil {
		return err
	}
	return printResidents(stdout, resp.Residents, jsonOut)
}

func printResidents(w io.Writer, rows []rpc.Resident, jsonOut bool) error {
	if rows == nil {
		rows = []rpc.Resident{}
	}
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"count": len(rows), "residents": rows})
	}
	fmt.Fprintf(w, "%d resident(s) whose figaro the store does not hold\n", len(rows))
	for _, r := range rows {
		what := "agent and endpoint"
		switch {
		case !r.Endpoint:
			what = "agent"
		case !r.Agent:
			what = "endpoint"
		}
		fmt.Fprintf(w, "%s  %s\n", r.ID, what)
	}
	return nil
}
