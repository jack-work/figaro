package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/transport"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/outfit"
)

// dressing is what the three dressing flags produced: the text as typed, for
// messages; the outfit NAMES `-O` asked for; and the patch `-S`/`-D` spelled
// out. Two axes, never mixed: names travel as names and the daemon's one
// dressing call resolves them, which is what lets `-O` mean the same thing on
// a live aria as at birth without a directive ever riding inside a patch.
type dressing struct {
	text  string
	names []string
	patch *rpc.FormPatch
}

func (d dressing) IsEmpty() bool {
	return len(d.names) == 0 && (d.patch == nil || d.patch.IsEmpty())
}

// label is the text, shortened. A literal can be kilobytes; a notice that
// reprints it is not a notice.
func (d dressing) label() string {
	if len(d.text) <= 72 {
		return d.text
	}
	return d.text[:69] + "..."
}

// parseDress reads the three flags into one dressing. Precedence is fixed and
// stated everywhere it matters: outfits fold first, then --set, then --delete.
// Any of the three may be empty.
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
	if strings.TrimSpace(set) != "" {
		p, err := outfit.ParseSet(set)
		if err != nil {
			return dressing{}, err
		}
		patch.Set = p.Set
		texts = append(texts, set)
	}
	if strings.TrimSpace(del) != "" {
		paths, err := outfit.ParseDelete(del)
		if err != nil {
			return dressing{}, err
		}
		patch.Remove = paths
		texts = append(texts, "-"+del)
	}
	if !patch.IsEmpty() {
		d.patch = &patch
	}
	d.text = strings.Join(texts, " ")
	return d, nil
}

// mustDress is parseDress for the flag trio, dying on a grammar error.
func mustDress(outfits, set, del string) dressing {
	d, err := parseDress(outfits, set, del)
	if err != nil {
		die("%s", err)
	}
	return d
}

// parseNames reads a positional or flag value that must be outfit NAMES -
// `state outfit a,b`, `cast -O role`. A `k=v` there is a grammar error that
// names the flag which takes it.
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

// softFetchOutfitNames asks the angelus what outfits exist, for completion.
// Returns nil on any failure: completion must never autostart the daemon,
// prompt, or block long.
func softFetchOutfitNames() []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath()))
	if err != nil {
		return nil
	}
	defer acli.Close()
	resp, err := acli.Outfits(ctx, "")
	if err != nil {
		return nil
	}
	return resp.Names
}
