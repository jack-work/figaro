package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

type formKeyRow struct {
	Path   string `json:"path"`
	Writer string `json:"writer"`
	Exact  bool   `json:"exact,omitempty"`
	Shape  string `json:"shape"`
	Doc    string `json:"doc,omitempty"`
}

type shapeFaultRow struct {
	Path    string   `json:"path"`
	Count   int      `json:"count"`
	Samples []string `json:"samples"`
	Example string   `json:"example"`
}

type shapeAudit struct {
	Forms     int             `json:"forms"`
	Unread    int             `json:"unread"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Faults    []shapeFaultRow `json:"faults"`
}

const auditConnections = 8

type formReader interface {
	ListGlobal(ctx context.Context) (*rpc.ListResponse, error)
	FormQuiet(ctx context.Context, figaroID string) (*rpc.FormResponse, error)
}

func runDoctorFormKeys(loaded *config.Loaded, audit, jsonOut bool) error {
	if !audit {
		return printFormKeys(stdout, jsonOut)
	}
	doors := make([]formReader, auditConnections)
	for i := range doors {
		acli := mustConnectAngelus(loaded)
		defer acli.Close()
		doors[i] = acli
	}
	rep, err := auditFormShapes(context.Background(), doors)
	if err != nil {
		return err
	}
	return printShapeAudit(stdout, rep, jsonOut)
}

func printFormKeys(w io.Writer, jsonOut bool) error {
	var rows []formKeyRow
	for _, s := range form.Registry() {
		rows = append(rows, formKeyRow{Path: s.Path, Writer: s.Writer.String(), Exact: s.Exact, Shape: s.Shape.Name, Doc: s.Short})
	}
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tWRITER\tSHAPE\tDOC")
	for _, r := range rows {
		doc := r.Doc
		if len(doc) > 70 {
			doc = doc[:67] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Path, r.Writer, r.Shape, doc)
	}
	return tw.Flush()
}

func auditFormShapes(ctx context.Context, doors []formReader) (shapeAudit, error) {
	start := time.Now()
	resp, err := doors[0].ListGlobal(ctx)
	if err != nil {
		return shapeAudit{}, err
	}
	type result struct {
		id     string
		faults []form.ShapeFault
		ok     bool
	}
	ids := make(chan string)
	results := make(chan result)
	var wg sync.WaitGroup
	for _, door := range doors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				got, err := door.FormQuiet(ctx, id)
				if err != nil || got == nil {
					results <- result{id: id}
					continue
				}
				results <- result{id: id, faults: form.AuditShapes(got.Snapshot), ok: true}
			}
		}()
	}
	go func() {
		for _, f := range resp.Figaros {
			if f.State != "anchor" {
				ids <- f.ID
			}
		}
		close(ids)
		wg.Wait()
		close(results)
	}()
	var rep shapeAudit
	byPath := map[string]*shapeFaultRow{}
	for r := range results {
		if !r.ok {
			rep.Unread++
			continue
		}
		rep.Forms++
		for _, fault := range r.faults {
			row := byPath[fault.Path]
			if row == nil {
				row = &shapeFaultRow{Path: fault.Path, Example: fault.Err.Error()}
				byPath[fault.Path] = row
			}
			row.Count++
			if len(row.Samples) < 3 {
				row.Samples = append(row.Samples, r.id)
			}
		}
	}
	for _, row := range byPath {
		rep.Faults = append(rep.Faults, *row)
	}
	sort.Slice(rep.Faults, func(i, j int) bool {
		if rep.Faults[i].Count != rep.Faults[j].Count {
			return rep.Faults[i].Count > rep.Faults[j].Count
		}
		return rep.Faults[i].Path < rep.Faults[j].Path
	})
	rep.ElapsedMS = time.Since(start).Milliseconds()
	return rep, nil
}

func printShapeAudit(w io.Writer, rep shapeAudit, jsonOut bool) error {
	if jsonOut {
		if rep.Faults == nil {
			rep.Faults = []shapeFaultRow{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tFORMS\tSAMPLES\tEXAMPLE")
	for _, f := range rep.Faults {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", f.Path, f.Count, strings.Join(f.Samples, ","), f.Example)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "%d forms read (%d unreadable) in %d ms; %d keys hold a value that fails its shape\n", rep.Forms, rep.Unread, rep.ElapsedMS, len(rep.Faults))
	return nil
}
