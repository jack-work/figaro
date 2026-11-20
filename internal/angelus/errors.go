package angelus

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/jkrpc"
)

func (h *handlers) errNoDefaultOutfit() error {
	loaded, _ := h.settings()
	data, _ := json.Marshal(rpc.ErrorData{AvailableProviders: h.availableProviders, ConfigPath: loaded.ConfigPath})
	return &jkrpc.Error{
		Code:    rpc.ErrNoDefaultOutfit,
		Message: "no default outfit configured",
		Data:    data,
	}
}

func (h *handlers) errNoProvider(outfitName string) error {
	loaded, _ := h.settings()
	missing, err := missingOutfits(loaded, outfitName)
	if err != nil {
		return fmt.Errorf("outfit %q sets no system.provider, and the outfits do not index: %w", outfitName, err)
	}
	data, _ := json.Marshal(rpc.ErrorData{
		AvailableProviders: h.availableProviders,
		Outfit:             outfitName,
		ConfigPath:         loaded.ConfigPath,
		Missing:            missing,
	})
	return &jkrpc.Error{
		Code:    rpc.ErrNoProvider,
		Message: fmt.Sprintf("outfit %q has no system.provider", outfitName),
		Data:    data,
	}
}

// errOutfitNotFound carries the whole closure when a layer reference is broken.
func (h *handlers) errOutfitNotFound(name string, cause error) error {
	payload := rpc.ErrorData{
		Name:        name,
		SearchPaths: []string{loadedOutfitPath(h, name)},
	}
	var missing *outfit.MissingError
	if errors.As(cause, &missing) {
		payload.OutfitClosure = figaro.OutfitClosureWire(missing.Closure)
	}
	data, _ := json.Marshal(payload)
	return &jkrpc.Error{
		Code:    rpc.ErrOutfitNotFound,
		Message: fmt.Sprintf("outfit %q not found: %s", name, cause),
		Data:    data,
	}
}

func missingOutfits(loaded *config.Loaded, spec string) ([]string, error) {
	idx, err := loaded.OutfitIndex()
	if err != nil {
		return nil, err
	}
	names, err := outfit.TermNames(spec)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, n := range names {
		if _, ok := idx[n]; !ok && n != outfitNameDefault {
			missing = append(missing, n)
		}
	}
	return missing, nil
}
