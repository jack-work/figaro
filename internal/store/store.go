// Package store defines the per-aria multi-column log: the canonical
// figaro IR Stream plus per-provider translator Streams.
//
// Each column is a Stream[T] (see stream.go). The figaro IR Stream is
// canonical; translator Streams cache per-provider wire-format
// projections, FK'd back via Entry.FigaroLT.
//
// Backend opens the per-aria handles and enumerates persisted arias.
// FileBackend is the default (NDJSON-per-line on disk).
package store

import (
	"encoding/json"

	"github.com/jack-work/figaro/internal/message"
)

// AriaMeta holds metadata persisted alongside an aria.
//
// Deprecated: scheduled to retire in Stage C of the log-unification
// work, when chalkboard.system.* keys take over the restoration
// metadata role. For now it lives in arias/{id}/meta.json.
type AriaMeta struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Cwd      string `json:"cwd"`
	Root     string `json:"root"`
	Label    string `json:"label,omitempty"`
}

// Backend is the aria storage provider. One per angelus lifetime.
//
// Owns the full set of persisted arias. Opens per-aria figaro IR
// streams and per-(aria, provider) translator streams. Implementations
// must be safe for concurrent use across arias.
type Backend interface {
	// Open returns the canonical figaro IR Stream for the aria. New
	// arias return an empty Stream; existing ones replay persisted
	// state.
	Open(ariaID string) (Stream[message.Message], error)

	// OpenTranslation returns the per-provider translator Stream
	// for the aria. Each (aria, provider) pair has its own column,
	// FK'd back to the figaro IR Stream via Entry.FigaroLT.
	OpenTranslation(ariaID, providerName string) (Stream[[]json.RawMessage], error)

	// Meta returns the aria metadata, or nil if unset. Persisted
	// alongside the figaro IR stream by the backend.
	Meta(ariaID string) (*AriaMeta, error)

	// SetMeta sets the aria metadata.
	SetMeta(ariaID string, meta *AriaMeta) error

	// List returns metadata for every persisted aria. Used by `figaro
	// list` (which merges this with live registry entries) and by
	// lazy restore-by-ID lookups.
	List() ([]AriaInfo, error)

	// Remove deletes an aria permanently. A missing aria is not an
	// error. Callers must close the owning agent (and therefore any
	// live Stream handles) before calling Remove to avoid racing with
	// a pending Append.
	Remove(ariaID string) error

	// Close releases backend-level resources. Callers must first close
	// all live Stream handles via the owning agents.
	Close() error
}
