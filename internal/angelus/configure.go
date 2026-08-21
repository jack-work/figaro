package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
)

// outfits answers what outfits exist and how a spec composes. The outfits
// directory is the server's state: a client asks rather than reading it, so the
// CLI needs no notion of where config lives and the daemon may sit on another
// filesystem entirely.
func (h *handlers) outfits(_ context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.OutfitsRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}
	loaded, ofit := h.settings()

	resp := rpc.OutfitsResponse{Default: loaded.Config.DefaultOutfit, Names: loaded.ListOutfits()}
	spec := req.Spec
	if strings.TrimSpace(spec) == "" {
		spec = resp.Default
	}
	if strings.TrimSpace(spec) == "" {
		return resp, nil
	}
	names, err := outfit.TermNames(spec)
	if err != nil {
		return nil, err
	}
	resp.Closure = figaro.OutfitClosureWire(ofit.ResolveAll(names))
	return resp, nil
}

// configure patches config.toml on the server's behalf. The first-run wizard is
// a client: it cannot write the daemon's config itself: so this is the seam
// it drives, and the only config the CLI may change.
func (h *handlers) configure(_ context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ConfigureRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	loaded, _ := h.settings()

	var resp rpc.ConfigureResponse
	resp.Refreshed = req.Refresh
	if req.Outfit != "" || req.Body != "" {
		if req.Outfit == "" || req.Body == "" {
			return nil, fmt.Errorf("configure: outfit and body must be set together")
		}
		if err := outfit.ValidName(req.Outfit); err != nil {
			return nil, err
		}
		path := loaded.OutfitPath(req.Outfit)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("configure: %w", err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return nil, fmt.Errorf("configure: write outfit: %w", err)
		}
		_, werr := f.WriteString(req.Body)
		cerr := f.Close()
		if werr != nil {
			return nil, fmt.Errorf("configure: write outfit: %w", werr)
		}
		if cerr != nil {
			return nil, fmt.Errorf("configure: write outfit: %w", cerr)
		}
		resp.OutfitPath = path
	}
	if req.DefaultOutfit != "" {
		if err := config.SetDefaultOutfit(loaded.ConfigPath, req.DefaultOutfit); err != nil {
			return nil, fmt.Errorf("configure: %w", err)
		}
		resp.DefaultOutfit = req.DefaultOutfit
	}
	// Re-read so the next Create sees what was just written, and turn the
	// resolver's epoch over so the next fold re-reads and re-snapshots what it
	// needs. Reload is cheap by design: it reads nothing, it only invalidates
	//: so a refresh no longer throws the resolver away and rebuilds it.
	if fresh, err := config.Load(loaded.ConfigDir); err == nil {
		h.configMu.Lock()
		h.config = fresh
		if h.outfitter != nil {
			h.outfitter.Reload()
		} else {
			h.outfitter = newOutfitter(h.angelus, fresh)
		}
		ofit := h.outfitter
		h.configMu.Unlock()
		ofit.Warm(fresh.Config.DefaultOutfit)
	}
	return resp, nil
}

// loadedOutfitPath is where an outfit of this name would live, for an error
// message. Read under the lock like everything else that touches config.
func loadedOutfitPath(h *handlers, name string) string {
	loaded, _ := h.settings()
	if loaded == nil {
		return name
	}
	return loaded.OutfitPath(name)
}
