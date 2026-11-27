package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

func runNote(loaded *config.Loaded, ariaID, text string, asJSON bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		dieUsage("note: usage is `figaro note [--id <aria>] -- <text>`")
	}
	if err := needCapability(rpc.CapNote, "figaro note"); err != nil {
		return err
	}
	s, ctx, cancel := openQueueSession(loaded, ariaID)
	defer cancel()
	defer s.done()

	resp, err := s.cli.Note(ctx, text)
	if err != nil {
		return fmt.Errorf("note %s: %w", s.id, err)
	}
	if asJSON {
		out, merr := json.Marshal(map[string]any{"aria": s.id, "key": resp.Key})
		if merr != nil {
			return merr
		}
		fmt.Fprintln(stdout, string(out))
		return nil
	}
	fmt.Fprintf(stdout, "note left for %s\n", ariaName(s.id))
	return nil
}
