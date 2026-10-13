package angelus

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/jkrpc"
)

func (h *handlers) errNoDefaultOutfit() error {
	data, _ := json.Marshal(rpc.ErrorData{AvailableProviders: h.availableProviders})
	return &jkrpc.Error{
		Code:    rpc.ErrNoDefaultOutfit,
		Message: "no default outfit configured",
		Data:    data,
	}
}

func (h *handlers) errNoProvider(outfitName string) error {
	data, _ := json.Marshal(rpc.ErrorData{
		AvailableProviders: h.availableProviders,
		Outfit:             outfitName,
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
