package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

// portableAria carries the messages verbatim and no node ids, LTs or fork
// bases: those belong to the store, not the aria.
type portableAria struct {
	Figaro    string            `json:"figaro"`
	AriaID    string            `json:"aria_id,omitempty"`
	Outfit    string            `json:"outfit"`
	Mantra    string            `json:"mantra,omitempty"`
	Provider  string            `json:"provider,omitempty"`
	Model     string            `json:"model,omitempty"`
	Exported  string            `json:"exported,omitempty"`
	BoundForm message.Patch     `json:"form,omitempty"`
	Messages  []message.Message `json:"messages"`
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
	fmt.Fprintf(stderrw, "exported %s (%d messages) to %s\n", ariaName(id), len(doc.Messages), out)
}

func exportMessages(ctx context.Context, acli *sdk.Angelus, id string) ([]message.Message, error) {
	resp, err := acli.IRAll(ctx, id, 0)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", id, err)
	}
	// An export is the only copy once the aria is deleted, so a short read is
	// an error (#23). Short only: a live aria can grow between the count and
	// the read.
	if len(resp.Entries) < resp.Total {
		return nil, fmt.Errorf("read %s: got %d of %d entries; refusing to write a partial export", id, len(resp.Entries), resp.Total)
	}
	msgs := make([]message.Message, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		var m message.Message
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			return nil, fmt.Errorf("parse LT=%d: %w", e.LT, err)
		}
		// Scaffolding does not travel: the genesis tic and a contentless input
		// tic belong to the store's topology.
		if m.Role == message.RoleGenesis || (m.Role == message.RoleInput && len(m.Content) == 0) {
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// exportAria goes through the angelus, so it works while the aria is live and
// never touches the store's flock.
func exportAria(ctx context.Context, acli *sdk.Angelus, loaded *config.Loaded, id string) (portableAria, error) {
	msgs, err := exportMessages(ctx, acli, id)
	if err != nil {
		return portableAria{}, err
	}
	doc := portableAria{
		Figaro:   portableFormat,
		AriaID:   id,
		Exported: time.Now().Format(time.RFC3339),
		Messages: msgs,
	}

	// The form lives on the aria socket, so this attaches and revives a
	// dormant aria.
	var bound form.Snapshot
	var boundFormErr error
	WithSessionFor(loaded, id, func(s *Session) error {
		resp, cErr := s.Figaro.Form(ctx)
		if cErr != nil {
			boundFormErr = cErr
			return nil
		}
		bound = resp.Snapshot
		return nil
	})
	if boundFormErr != nil {
		return portableAria{}, fmt.Errorf("form %s: %w", id, boundFormErr)
	}
	keys := map[string]json.RawMessage{}
	for k, v := range bound.All() {
		// The destination stamps its own id.
		if k == "aria_id" {
			continue
		}
		keys[k] = v
	}
	doc.BoundForm = form.Build(form.Snapshot{}, keys, nil)
	if raw, ok := bound.Get("system.outfit_name"); ok {
		_ = json.Unmarshal(raw, &doc.Outfit)
	}
	if doc.Outfit == "" {
		if raw, ok := bound.Get("system.loadout_name"); ok {
			_ = json.Unmarshal(raw, &doc.Outfit)
		}
	}
	if raw, ok := bound.Get("mantra"); ok {
		_ = json.Unmarshal(raw, &doc.Mantra)
	}
	if raw, ok := bound.Get("system.provider"); ok {
		_ = json.Unmarshal(raw, &doc.Provider)
	}
	if raw, ok := bound.Get("system.model"); ok {
		_ = json.Unmarshal(raw, &doc.Model)
	}
	if doc.Outfit == "" {
		doc.Outfit = "imported"
	}
	return doc, nil
}

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
		Form:     doc.BoundForm,
		Messages: doc.Messages,
		WasID:    doc.AriaID,
		Mantra:   doc.Mantra,
		Provider: doc.Provider,
		Model:    doc.Model,
	})
	if err != nil {
		die("import: %s", err)
	}
	fmt.Fprintf(stderrw, "imported %d messages as %s (outfit %s)\n",
		resp.Messages, ariaName(resp.FigaroID), resp.Outfit)
	if resp.WasID != "" {
		fmt.Fprintf(stderrw, "  it was %s where it came from: a trunk id is unique per store, so this one is new\n", resp.WasID)
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
