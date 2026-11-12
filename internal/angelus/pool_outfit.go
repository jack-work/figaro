package angelus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
)

var poolTools = []string{"say", "search"}

type pinnedPoolOutfit struct {
	name  string
	hash  string
	patch form.Patch
}

type poolOutfits map[string]pinnedOrRefused

type pinnedOrRefused struct {
	pinned  pinnedPoolOutfit
	refused error
}

var whatAGuestOutfitCannotCarry = []struct{ key, why string }{
	{"skills", "a pool figaro carries no skill"},
	{"system.cwd", "it is a path on the host"},
	{"system.environment", "it reaches the daemon's environment and can name a wire dump directory"},
	{"system.methods", "a pool figaro asks no decision model"},
	{"system.decide", "a pool figaro asks no decision model"},
	{"system.studies", "a pool figaro studies no form"},
	{"system.retirement.ramp", "it names a file on the host"},
	{"system.retirement.page", "a pool pages through the broker and never a figaro"},
	{"system.sandbox", "the lease stamps it"},
	{"system.scopes", "the lease stamps it"},
}

func pinPoolOutfit(ofit *outfit.Outfitter, pool config.PoolConfig) (pinnedPoolOutfit, error) {
	name := pool.Outfit
	if ofit == nil {
		return pinnedPoolOutfit{}, fmt.Errorf("outfit %s: no outfit directory configured", name)
	}
	root := ofit.Resolve(name)
	switch {
	case root == nil || !root.Found:
		return pinnedPoolOutfit{}, fmt.Errorf("outfit %s is missing", name)
	case root.Err != nil:
		return pinnedPoolOutfit{}, root.Err
	case len(root.Layers) > 0:
		return pinnedPoolOutfit{}, fmt.Errorf("outfit %s has layers: a pool's outfit is one file, read whole", name)
	}
	patch, err := ofit.Load(name)
	if err != nil {
		return pinnedPoolOutfit{}, err
	}
	raw, err := json.Marshal(form.Snapshot{}.Apply(patch))
	if err != nil {
		return pinnedPoolOutfit{}, fmt.Errorf("outfit %s: %w", name, err)
	}
	var folded map[string]any
	if err := json.Unmarshal(raw, &folded); err != nil {
		return pinnedPoolOutfit{}, fmt.Errorf("outfit %s: %w", name, err)
	}
	if err := refuseWhatAGuestOutfitCannotCarry(folded, pool.Provider); err != nil {
		return pinnedPoolOutfit{}, fmt.Errorf("outfit %s %w", name, err)
	}
	canonical, err := json.Marshal(folded)
	if err != nil {
		return pinnedPoolOutfit{}, fmt.Errorf("outfit %s: %w", name, err)
	}
	sum := sha256.Sum256(canonical)
	return pinnedPoolOutfit{name: name, hash: hex.EncodeToString(sum[:]), patch: patch}, nil
}

func (p pinnedPoolOutfit) foldsAsPinned(ofit *outfit.Outfitter, pool config.PoolConfig) error {
	now, err := pinPoolOutfit(ofit, pool)
	if err != nil {
		return err
	}
	if now.hash != p.hash {
		return fmt.Errorf("outfit %s changed since its pool loaded: a lease mints the outfit that was checked, so reload the config to take the new one", p.name)
	}
	return nil
}

func refuseWhatAGuestOutfitCannotCarry(folded map[string]any, provider string) error {
	var refusal error
	var walk func(path string, v any)
	walk = func(path string, v any) {
		if refusal != nil {
			return
		}
		for _, f := range whatAGuestOutfitCannotCarry {
			if path == f.key || strings.HasPrefix(path, f.key+".") {
				refusal = fmt.Errorf("carries %s: %s", path, f.why)
				return
			}
		}
		if path == "filePath" || strings.HasSuffix(path, ".filePath") {
			refusal = fmt.Errorf("carries %s: it is a path on the host", path)
			return
		}
		switch v := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if path == "" {
					walk(k, v[k])
				} else {
					walk(path+"."+k, v[k])
				}
			}
		case []any:
			for _, item := range v {
				walk(path+"[]", item)
			}
		}
	}
	walk("", folded)
	if refusal != nil {
		return refusal
	}
	system, _ := folded["system"].(map[string]any)
	tools, listed := system["tools"].([]any)
	if !listed {
		return errors.New("has no system.tools list: with none, a figaro is offered every tool there is")
	}
	for _, t := range tools {
		name, ok := t.(string)
		if !ok || !slices.Contains(poolTools, name) {
			return fmt.Errorf("lists the tool %v: a pool figaro has %s and no other", t, strings.Join(poolTools, " and "))
		}
	}
	if got, _ := system["provider"].(string); got != provider {
		return fmt.Errorf("sets system.provider = %q and its pool's provider is %q: the leased box allows the pool's alone, so no lease could mint", got, provider)
	}
	return nil
}

func pinPoolOutfits(loaded *config.Loaded, ofit *outfit.Outfitter) poolOutfits {
	out := poolOutfits{}
	for _, name := range loaded.Pools() {
		pool, _ := loaded.Pool(name)
		pinned, err := pinPoolOutfit(ofit, pool)
		if err != nil {
			slog.Error("pool does not load and its leases are refused", "pool", name, "err", err)
		}
		out[name] = pinnedOrRefused{pinned: pinned, refused: err}
	}
	return out
}

func (h *handlers) poolOutfit(pool string) (pinnedPoolOutfit, error) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	p, ok := h.pools[pool]
	switch {
	case !ok:
		return pinnedPoolOutfit{}, fmt.Errorf("no pool %q", pool)
	case p.refused != nil:
		return pinnedPoolOutfit{}, fmt.Errorf("pool %s did not load: %w", pool, p.refused)
	}
	return p.pinned, nil
}
