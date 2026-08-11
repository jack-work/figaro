package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
)

// Moving an aria between stores.
//
// The store is ONE figwal trunk store, not a directory per aria: node ids are
// positional in a flat namespace, fork bases are indices into a parent's log,
// and a trunk id is unique per store. So two stores cannot simply be poured
// together: the identities collide, and a collision is SILENT, giving a store
// that opens, lists and renders subtly wrong.
//
// Export/import sidesteps all of it by carrying CONTENT rather than identity.
// The destination mints its own node ids, its own fork bases and its own LTs
// through the ordinary spawn path, so nothing can collide by construction, and
// the failure mode is a refusal rather than a corruption.
//
// What that costs, stated plainly: the provider translation caches do not
// travel, so the first turn after an import re-translates (and replays without
// thinking blocks rather than with unsigned ones). Exact fidelity: node ids,
// LTs, branches, the wire caches: is the graft's job, and the graft is a
// design in proposals/aria-graft.md rather than code.

// portableAria is the file format. Deliberately plain: a JSON object a human
// can read, diff and hand-edit, with the messages verbatim as they sit in the
// IR. It carries no node ids, no LTs and no fork bases, because those belong
// to the store an aria happens to be in and not to the aria.
type portableAria struct {
	Figaro   string            `json:"figaro"`             // format marker + version
	AriaID   string            `json:"aria_id,omitempty"`  // the id it had; offered, not demanded
	Outfit   string            `json:"outfit"`             // the outfit name to resolve or create
	Mantra   string            `json:"mantra,omitempty"`   // for the human reading the file
	Provider string            `json:"provider,omitempty"` // provenance, for the list sidecar
	Model    string            `json:"model,omitempty"`
	Exported string            `json:"exported,omitempty"` // RFC3339, provenance only
	Board    message.Patch     `json:"form,omitempty"`
	Messages []message.Message `json:"messages"`
}

// UnmarshalJSON accepts the pre-rename "loadout" field; only "outfit" is written.
func (d *portableAria) UnmarshalJSON(b []byte) error {
	type alias portableAria
	var wire struct {
		alias
		LegacyOutfit string `json:"loadout,omitempty"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return err
	}
	*d = portableAria(wire.alias)
	if d.Outfit == "" {
		d.Outfit = wire.LegacyOutfit
	}
	return nil
}

const portableFormat = "aria/v1"

// runExport writes an aria to a file (or stdout) in the portable format.
func runExport(loaded *config.Loaded, args []string) {
	id, out := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--out":
			if i+1 >= len(args) {
				die("export: %s requires a path", args[i])
			}
			out = args[i+1]
			i++
		default:
			if id != "" {
				die("export: unexpected argument %q", args[i])
			}
			id = args[i]
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	if id == "" {
		r, err := resolveBinding(ctx, acli, shellPID)
		if err != nil || !r.Found {
			die("export: no aria bound to this shell (try: figaro export <id>)")
		}
		id = r.FigaroID
	}

	doc, err := exportAria(ctx, acli, loaded, id)
	if err != nil {
		die("export: %s", err)
	}
	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		die("export: %s", err)
	}
	blob = append(blob, '\n')
	if out == "" {
		os.Stdout.Write(blob)
		return
	}
	if err := os.WriteFile(out, blob, 0o600); err != nil {
		die("export: %s", err)
	}
	fmt.Fprintf(os.Stderr, "exported %s (%d messages) to %s\n", id, len(doc.Messages), out)
}

// exportAria gathers everything portable about one aria, through the angelus -
// so it works while the aria is live, and never touches the store's flock.
func exportAria(ctx context.Context, acli *angelus.Client, loaded *config.Loaded, id string) (portableAria, error) {
	resp, err := acli.AriaRead(ctx, id, 0, 0)
	if err != nil {
		return portableAria{}, fmt.Errorf("read %s: %w", id, err)
	}
	doc := portableAria{
		Figaro:   portableFormat,
		AriaID:   id,
		Exported: time.Now().Format(time.RFC3339),
		Messages: make([]message.Message, 0, len(resp.Entries)),
	}
	for _, e := range resp.Entries {
		var m message.Message
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			return portableAria{}, fmt.Errorf("parse LT=%d: %w", e.LT, err)
		}
		// Scaffolding does not travel. The genesis tic and the outfit-birth
		// stamp belong to the store's topology: the destination mints its own
		// when it spawns, and a contentless input tic is a pure form
		// carrier, whose effect is already in the folded board below. What is
		// left is the conversation.
		if m.Role == message.RoleGenesis || (m.Role == message.RoleInput && len(m.Content) == 0) {
			continue
		}
		doc.Messages = append(doc.Messages, m)
	}

	// The form lives on the ARIA socket, not the angelus, so this
	// attaches: which also revives a dormant aria, exactly as `figaro state`
	// does. It is the same fold the agent itself would read.
	var board form.Snapshot
	var boardErr error
	WithSessionFor(loaded, id, func(s *Session) error {
		resp, cErr := s.Figaro.Form(ctx)
		if cErr != nil {
			boardErr = cErr
			return nil
		}
		board = resp.Snapshot
		return nil
	})
	if boardErr != nil {
		return portableAria{}, fmt.Errorf("form %s: %w", id, boardErr)
	}
	doc.Board = message.Patch{Set: map[string]json.RawMessage{}}
	for k, v := range board.All() {
		// The destination stamps its own id; carrying this one would lie.
		if k == "aria_id" {
			continue
		}
		doc.Board.Set[k] = v
	}
	if raw, ok := board.Get("system.outfit_name"); ok {
		_ = json.Unmarshal(raw, &doc.Outfit)
	}
	if doc.Outfit == "" {
		if raw, ok := board.Get("system.loadout_name"); ok {
			_ = json.Unmarshal(raw, &doc.Outfit)
		}
	}
	if raw, ok := board.Get("mantra"); ok {
		_ = json.Unmarshal(raw, &doc.Mantra)
	}
	if raw, ok := board.Get("system.provider"); ok {
		_ = json.Unmarshal(raw, &doc.Provider)
	}
	if raw, ok := board.Get("system.model"); ok {
		_ = json.Unmarshal(raw, &doc.Model)
	}
	if doc.Outfit == "" {
		doc.Outfit = "imported"
	}
	return doc, nil
}

// runImport restores an exported aria as a new conversation in this store.
func runImport(loaded *config.Loaded, args []string) {
	path := ""
	for _, a := range args {
		if a == "" {
			continue
		}
		if path != "" {
			die("import: unexpected argument %q", a)
		}
		path = a
	}
	blob := []byte{}
	var err error
	if path == "" || path == "-" {
		blob, err = readAllStdin()
	} else {
		blob, err = os.ReadFile(path)
	}
	if err != nil {
		die("import: %s", err)
	}
	var doc portableAria
	if err := json.Unmarshal(blob, &doc); err != nil {
		die("import: %s (is this a `figaro export` file?)", err)
	}
	if doc.Figaro != portableFormat {
		die("import: not an aria export (want %q, got %q)", portableFormat, doc.Figaro)
	}
	if len(doc.Messages) == 0 {
		die("import: the export carries no messages")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resp, err := acli.Import(ctx, rpc.ImportRequest{
		Outfit:   doc.Outfit,
		Form:     doc.Board,
		Messages: doc.Messages,
		WasID:    doc.AriaID,
		Mantra:   doc.Mantra,
		Provider: doc.Provider,
		Model:    doc.Model,
	})
	if err != nil {
		die("import: %s", err)
	}
	fmt.Fprintf(os.Stderr, "imported %d messages as %s (outfit %s)\n",
		resp.Messages, resp.FigaroID, resp.Outfit)
	if resp.WasID != "" {
		fmt.Fprintf(os.Stderr, "  it was %s where it came from: a trunk id is unique per store, so this one is new\n", resp.WasID)
	}
}

func readAllStdin() ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		n, err := os.Stdin.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, nil
		}
	}
}
