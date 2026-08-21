package angelus

// The errors a first-run or misconfigured store produces, written once and
// at length: each of these is read by a human who is stuck.
//
// Split out of protocol.go, which had grown to 2,011 lines and answered every
// question at once. Same package, same behaviour: only the reader's job
// changes. plans/api-coherence.md step 5.

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/jkrpc"
)

// errNoDefaultOutfit builds a typed JSON-RPC error directing the
// client to drive first-run outfit selection.
func (h *handlers) errNoDefaultOutfit() error {
	data, _ := json.Marshal(rpc.ErrorData{AvailableProviders: h.availableProviders})
	return &jkrpc.Error{
		Code:    rpc.ErrNoDefaultOutfit,
		Message: "no default outfit configured",
		Data:    data,
	}
}

// errNoProvider builds a typed JSON-RPC error indicating the
// resolved outfit has no system.provider key.
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

// errOutfitNotFound builds a typed JSON-RPC error for a missing
// named outfit. cause carries the underlying outfit error, and when that names
// a broken layer reference the whole closure travels with it, so the caller can
// draw where the gap is.
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
