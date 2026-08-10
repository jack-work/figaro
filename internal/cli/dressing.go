package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/transport"

	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/rpc"
)

// dressing is what `-O` produced: the text as typed, for messages, and the
// chalkboard patch it parsed into, which is what travels.
//
// No disk and no config is read here. A name becomes an entry in the patch's
// `layers` directive and the SERVER resolves it — which is what lets `-O` mean
// the same thing on a live aria as at birth.
type dressing struct {
	text  string
	patch *rpc.ChalkboardPatch
}

func (d dressing) IsEmpty() bool { return d.patch == nil || d.patch.IsEmpty() }

// label is the text, shortened. A literal can be kilobytes; a notice that
// reprints it is not a notice.
func (d dressing) label() string {
	if len(d.text) <= 72 {
		return d.text
	}
	return d.text[:69] + "..."
}

func parseDressing(text string) (dressing, error) {
	if strings.TrimSpace(text) == "" {
		return dressing{}, fmt.Errorf("--outfit requires a value")
	}
	patch, err := outfit.ParsePatch(text)
	if err != nil {
		return dressing{}, err
	}
	if patch.IsEmpty() {
		return dressing{}, fmt.Errorf("--outfit %q names nothing", text)
	}
	return dressing{text: text, patch: &patch}, nil
}

// mustParseDressing is parseDressing for a positional argument.
func mustParseDressing(arg, usage string) dressing {
	if strings.TrimSpace(arg) == "" {
		die("usage: %s", usage)
	}
	d, err := parseDressing(arg)
	if err != nil {
		die("%s", err)
	}
	return d
}

// softFetchOutfitNames asks the angelus what outfits exist, for completion.
// Returns nil on any failure: completion must never autostart the daemon,
// prompt, or block long.
func softFetchOutfitNames() []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := angelus.DialClient(transport.UnixEndpoint(angelusSocketPath()))
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
