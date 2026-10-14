package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/sdk"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/transport"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/outfit"
)

// dressing keeps outfit names and the patch apart: names travel as names and the
// daemon resolves them.
type dressing struct {
	text  string
	names []string
	patch *rpc.FormPatch
}

func (d dressing) IsEmpty() bool {
	return len(d.names) == 0 && (d.patch == nil || d.patch.IsIdentity())
}

func (d dressing) label() string {
	if len(d.text) <= 72 {
		return d.text
	}
	return d.text[:69] + "..."
}

// parseDress: outfits fold first, then --set, then --delete.
func parseDress(outfits, set, del string) (dressing, error) {
	d := dressing{}
	var texts []string
	if strings.TrimSpace(outfits) != "" {
		names, err := outfit.ParseNames(outfits)
		if err != nil {
			return dressing{}, err
		}
		d.names = names
		texts = append(texts, outfits)
	}
	patch := rpc.FormPatch{}
	keys := map[string]json.RawMessage{}
	var drops []string
	if strings.TrimSpace(set) != "" {
		p, err := outfit.ParseSet(set)
		if err != nil {
			return dressing{}, err
		}
		for _, e := range p.Entries() {
			if !e.IsRemoval() {
				keys[e.Key] = e.New
			}
		}
		texts = append(texts, set)
	}
	if strings.TrimSpace(del) != "" {
		paths, err := outfit.ParseDelete(del)
		if err != nil {
			return dressing{}, err
		}
		drops = paths
		texts = append(texts, "-"+del)
	}
	patch = form.Build(form.Snapshot{}, keys, drops)
	if !patch.IsIdentity() {
		d.patch = &patch
	}
	d.text = strings.Join(texts, " ")
	return d, nil
}

func mustDress(outfits, set, del string) dressing {
	d, err := parseDress(outfits, set, del)
	if err != nil {
		die("%s", err)
	}
	return d
}

func parseNames(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("--outfit requires a value")
	}
	names, err := outfit.ParseNames(text)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("--outfit %q names nothing", text)
	}
	return names, nil
}

// softFetchOutfitNames never autostarts the daemon, prompts, or blocks long.
func softFetchOutfitNames() []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(boot.AngelusSocketPath()))
	if err != nil {
		// No daemon is not no outfits: they are files in the config dir.
		return outfitNamesOnDisk()
	}
	defer acli.Close()
	resp, err := acli.Outfits(ctx, "")
	if err != nil {
		return outfitNamesOnDisk()
	}
	return resp.Names
}

func outfitNamesOnDisk() []string {
	loaded, _ := config.Load(config.DefaultConfigDir())
	if loaded == nil {
		return nil
	}
	return loaded.ListOutfits()
}
