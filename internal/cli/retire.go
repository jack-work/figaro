package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

func ttlFromNow(createdAtMS int64, d time.Duration, now time.Time) string {
	var age time.Duration
	if createdAtMS > 0 && now.UnixMilli() > createdAtMS {
		age = now.Sub(time.UnixMilli(createdAtMS))
	}
	return (age + d).Round(time.Second).String()
}

func retirePatch(d dressing, undo bool, now time.Time) rpc.FormPatch {
	keys := map[string]json.RawMessage{}
	var drops []string
	if d.patch != nil {
		for _, e := range d.patch.Entries() {
			if e.IsRemoval() {
				drops = append(drops, e.Key)
				continue
			}
			keys[e.Key] = e.New
		}
	}
	if undo {
		delete(keys, store.SystemRetiredKey)
		drops = append(drops, store.SystemRetiredKey)
	} else {
		stamp, _ := json.Marshal(now.UTC().Format(time.RFC3339))
		keys[store.SystemRetiredKey] = stamp
	}
	return form.Build(form.Snapshot{}, keys, drops)
}

func runRetire(loaded *config.Loaded, idFlag string, args []string, outfits, set, del, ttl string, undo, asJSON bool) error {
	id := idFlag
	if id == "" && len(args) > 0 {
		id = args[0]
	}
	if id == "" {
		dieUsage("usage: figaro retire [--id <aria> | <aria>] [-O <outfits>] [-S <keys>] [-D <paths>] [--undo] [-j]")
	}
	d, err := parseDress(outfits, set, del)
	if err != nil {
		return fmt.Errorf("retire: %w", err)
	}
	var grace time.Duration
	if ttl != "" {
		if grace, err = store.ParseTTL(ttl); err != nil || grace <= 0 {
			return fmt.Errorf("retire: --ttl %q: want a positive duration (90m, 6h, 2d)", ttl)
		}
	}
	var resp *rpc.SetResponse
	var canonical string
	WithSessionFor(loaded, id, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		now := time.Now()
		patch := retirePatch(d, undo, now)
		if grace > 0 {
			created, err := createdAt(ctx, s, s.AriaID)
			if err != nil {
				die("retire: %s", err)
			}
			raw, _ := json.Marshal(ttlFromNow(created, grace, now))
			patch = form.Merge(patch, form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemTTLKey: raw}, nil))
		}
		r, err := s.Figaro.SetDressed(ctx, d.names, patch, 0)
		if err != nil {
			die("retire: %s", err)
		}
		resp, canonical = r, s.AriaID
		return nil
	})
	verb := "retired"
	if grace > 0 && !undo {
		verb = "retired (reaped " + shortDuration(grace) + " from now unless held)"
	}
	if undo {
		verb = "unretired"
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"id": canonical, "retired": !undo, "set": resp.Set, "version": resp.Version})
	}
	fmt.Fprintf(stderrw, "%s %s", verb, ariaName(canonical))
	if d.text != "" {
		fmt.Fprintf(stderrw, " (%s)", d.text)
	}
	fmt.Fprintln(stderrw)
	return nil
}

func createdAt(ctx context.Context, s *Session, id string) (int64, error) {
	resp, err := s.Angelus.ListGlobal(ctx)
	if err != nil {
		return 0, err
	}
	for _, f := range resp.Figaros {
		if f.ID == id {
			return f.CreatedAt, nil
		}
	}
	return 0, fmt.Errorf("%s is not in the listing", id)
}
