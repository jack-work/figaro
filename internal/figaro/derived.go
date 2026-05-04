package figaro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// DurableDerivation is the per-aria worker for a registered
// derivation. OnTick runs from a single goroutine, one event at a
// time. Write derived bytes to w; the framework atomically replaces
// the destination file when OnTick returns nil. Anything written on
// error is discarded.
type DurableDerivation interface {
	OnTick(w io.Writer, evt DerivationEvent) error
}

// DerivationEvent is one tick. Snapshot is the chalkboard state at
// tick-emission time (a defensive clone, safe to read off-goroutine).
type DerivationEvent struct {
	FigaroLT uint64
	Snapshot chalkboard.Snapshot
}

// DurDerivDeps is what Make / Resolve receive — the per-aria slice
// of construction state.
type DurDerivDeps struct {
	AriaID       string
	ProviderName string
	FigStream    store.Stream[message.Message]
	Translator   store.Stream[[]json.RawMessage]
}

// DurDerivReg registers a durable derivation under an alias.
//
//   - Alias is the CLI key: figaro -s <Alias>.
//   - Filename is the path relative to arias/<id>/, e.g. "meta.json"
//     or "derived/usage.json". Static.
//   - Resolve, when set, takes precedence over Filename and lets
//     the registration compute a dynamic destination from per-aria
//     deps (used for translations/<provider>.meta.json).
//   - Make is the per-aria factory.
type DurDerivReg struct {
	Alias    string
	Filename string
	Resolve  func(DurDerivDeps) string
	Make     func(DurDerivDeps) DurableDerivation
}

func (r DurDerivReg) filenameFor(d DurDerivDeps) string {
	if r.Resolve != nil {
		return r.Resolve(d)
	}
	return r.Filename
}

var (
	regsMu sync.RWMutex
	regs   []DurDerivReg
)

// Register adds a derivation to the global registry. Call from
// init() in derivation packages.
func Register(reg DurDerivReg) {
	if reg.Alias == "" || (reg.Filename == "" && reg.Resolve == nil) || reg.Make == nil {
		panic(fmt.Sprintf("durable derivation registration missing fields: %+v", reg))
	}
	regsMu.Lock()
	defer regsMu.Unlock()
	for _, r := range regs {
		if r.Alias == reg.Alias {
			panic(fmt.Sprintf("duplicate durable derivation alias: %q", reg.Alias))
		}
	}
	regs = append(regs, reg)
}

// Registrations returns a snapshot of the registered derivations.
func Registrations() []DurDerivReg {
	regsMu.RLock()
	defer regsMu.RUnlock()
	out := make([]DurDerivReg, len(regs))
	copy(out, regs)
	return out
}

// LookupRegistration returns the registration with the given alias,
// or false.
func LookupRegistration(alias string) (DurDerivReg, bool) {
	regsMu.RLock()
	defer regsMu.RUnlock()
	for _, r := range regs {
		if r.Alias == alias {
			return r, true
		}
	}
	return DurDerivReg{}, false
}

// AriaDir returns the per-aria directory (arias/<id>/) for a
// FileBackend, "" otherwise.
func AriaDir(backend store.Backend, ariaID string) string {
	fb, ok := backend.(interface{ Dir() string })
	if !ok {
		return ""
	}
	return filepath.Join(fb.Dir(), ariaID)
}

// DerivationFilePath joins ariaDir with a registration's resolved
// filename. Used by the CLI to find a derivation's on-disk file.
func DerivationFilePath(backend store.Backend, deps DurDerivDeps, reg DurDerivReg) string {
	dir := AriaDir(backend, deps.AriaID)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, reg.filenameFor(deps))
}

// derivationLoop is one goroutine + coalescing inbox per
// (figaro, registration). Owns the destination file path.
type derivationLoop struct {
	alias string
	path  string
	impl  DurableDerivation

	inbox chan DerivationEvent
	done  chan struct{}
}

func startLoop(ctx context.Context, alias, path string, impl DurableDerivation) *derivationLoop {
	l := &derivationLoop{
		alias: alias,
		path:  path,
		impl:  impl,
		inbox: make(chan DerivationEvent, 1),
		done:  make(chan struct{}),
	}
	go l.run(ctx)
	return l
}

func (l *derivationLoop) tick(evt DerivationEvent) {
	select {
	case l.inbox <- evt:
	default:
	}
}

func (l *derivationLoop) run(ctx context.Context) {
	defer close(l.done)
	for {
		select {
		case <-ctx.Done():
			// Drain pending ticks (select picks pseudo-randomly
			// when both cases are ready, so a queued tick could
			// otherwise be lost).
			for {
				select {
				case evt := <-l.inbox:
					l.process(evt)
				default:
					return
				}
			}
		case evt := <-l.inbox:
			l.process(evt)
		}
	}
}

func (l *derivationLoop) process(evt DerivationEvent) {
	var buf bytes.Buffer
	if err := l.impl.OnTick(&buf, evt); err != nil {
		fmt.Fprintf(os.Stderr, "derivation %s: ontick: %v\n", l.alias, err)
		return
	}
	if err := writeAtomic(l.path, buf.Bytes()); err != nil {
		// ENOENT means the parent dir was removed under us (test
		// teardown of t.TempDir, an aria deletion, etc). Silent.
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "derivation %s: write %s: %v\n", l.alias, l.path, err)
		}
	}
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// derivedFanout owns all per-figaro derivation loops. The agent
// holds one and ticks it after condense / endTurn.
type derivedFanout struct {
	loops []*derivationLoop
}

// startDerived spins up one loop per registration. Returns nil when
// the backend isn't file-backed.
func startDerived(
	ctx context.Context,
	ariaID, providerName string,
	backend store.Backend,
	figStream store.Stream[message.Message],
	translator store.Stream[[]json.RawMessage],
) *derivedFanout {
	dir := AriaDir(backend, ariaID)
	if dir == "" {
		return nil
	}
	deps := DurDerivDeps{
		AriaID:       ariaID,
		ProviderName: providerName,
		FigStream:    figStream,
		Translator:   translator,
	}
	rs := Registrations()
	if len(rs) == 0 {
		return nil
	}
	f := &derivedFanout{}
	for _, r := range rs {
		f.loops = append(f.loops, startLoop(ctx, r.Alias, filepath.Join(dir, r.filenameFor(deps)), r.Make(deps)))
	}
	return f
}

func (f *derivedFanout) Tick(figaroLT uint64, snap chalkboard.Snapshot) {
	if f == nil {
		return
	}
	evt := DerivationEvent{FigaroLT: figaroLT, Snapshot: snap}
	for _, l := range f.loops {
		l.tick(evt)
	}
}

func (f *derivedFanout) Wait() {
	if f == nil {
		return
	}
	for _, l := range f.loops {
		<-l.done
	}
}

// --- Built-in derivations ---

func init() {
	Register(DurDerivReg{
		Alias:    "summary",
		Filename: "meta.json",
		Make: func(d DurDerivDeps) DurableDerivation {
			return &summaryDerivation{figStream: d.FigStream}
		},
	})
	Register(DurDerivReg{
		Alias: "translator",
		Resolve: func(d DurDerivDeps) string {
			return filepath.Join("translations", d.ProviderName+".meta.json")
		},
		Make: func(d DurDerivDeps) DurableDerivation {
			return &translatorDerivation{providerName: d.ProviderName, translator: d.Translator}
		},
	})
	Register(DurDerivReg{
		Alias:    "usage",
		Filename: "derived/usage.json",
		Make: func(d DurDerivDeps) DurableDerivation {
			return &usageDerivation{
				ariaID:       d.AriaID,
				providerName: d.ProviderName,
				figStream:    d.FigStream,
			}
		},
	})
}

// summaryDerivation writes arias/<id>/meta.json — the AriaMeta
// shape, consumed by `figaro list`.
type summaryDerivation struct {
	figStream store.Stream[message.Message]
}

func (s *summaryDerivation) OnTick(w io.Writer, evt DerivationEvent) error {
	now := time.Now().UnixMilli()
	out := store.AriaMeta{LastActiveMS: now, LastFigaroLT: evt.FigaroLT}
	for _, e := range s.figStream.Durable() {
		out.MessageCount++
		m := e.Payload
		if m.Role == message.RoleAssistant {
			out.TurnCount++
		}
		if m.Usage != nil {
			out.TokensIn += m.Usage.InputTokens
			out.TokensOut += m.Usage.OutputTokens
			out.CacheReadTokens += m.Usage.CacheReadTokens
			out.CacheWriteTokens += m.Usage.CacheWriteTokens
		}
	}
	return json.NewEncoder(w).Encode(out)
}

// translatorDerivation writes per-provider translator stats to
// arias/<id>/translations/<provider>.meta.json.
type translatorDerivation struct {
	providerName string
	translator   store.Stream[[]json.RawMessage]
}

func (t *translatorDerivation) OnTick(w io.Writer, evt DerivationEvent) error {
	out := store.TranslationMeta{Provider: t.providerName, LastUpdateMS: time.Now().UnixMilli()}
	if t.translator != nil {
		entries := t.translator.Durable()
		for _, e := range entries {
			for _, p := range e.Payload {
				out.TotalBytes += len(p)
			}
			if e.Fingerprint != "" {
				out.Fingerprint = e.Fingerprint
			}
			if e.LT > out.LastTransLT {
				out.LastTransLT = e.LT
			}
		}
		out.EntryCount = len(entries)
	}
	return json.NewEncoder(w).Encode(out)
}

// usageDerivation writes arias/<id>/derived/usage.json — token +
// cache stats, consumed by `figaro -s usage`.
type usageDerivation struct {
	ariaID       string
	providerName string
	figStream    store.Stream[message.Message]
}

// Usage is the on-disk shape of usage.json.
type Usage struct {
	AriaID           string `json:"aria_id"`
	Provider         string `json:"provider,omitempty"`
	MessageCount     int    `json:"message_count"`
	TurnCount        int    `json:"turn_count"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	LastFigaroLT     uint64 `json:"last_figaro_lt,omitempty"`
	LastUpdateMS     int64  `json:"last_update_ms"`
}

func (u *usageDerivation) OnTick(w io.Writer, evt DerivationEvent) error {
	out := Usage{
		AriaID:       u.ariaID,
		Provider:     u.providerName,
		LastFigaroLT: evt.FigaroLT,
		LastUpdateMS: time.Now().UnixMilli(),
	}
	for _, e := range u.figStream.Durable() {
		out.MessageCount++
		m := e.Payload
		if m.Role == message.RoleAssistant {
			out.TurnCount++
		}
		if m.Usage != nil {
			out.TokensIn += m.Usage.InputTokens
			out.TokensOut += m.Usage.OutputTokens
			out.CacheReadTokens += m.Usage.CacheReadTokens
			out.CacheWriteTokens += m.Usage.CacheWriteTokens
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
