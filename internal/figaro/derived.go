package figaro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

// DurableDerivation is a per-aria derivation worker. OnTick writes
// derived bytes to w; the file is atomically replaced on nil error.
type DurableDerivation interface {
	OnTick(w io.Writer, evt DerivationEvent) error
}

// TODO: give derivations access to the full figaro log, not just
// the chalkboard snapshot. The log is append-only so a read cursor
// would be safe.
//
// DerivationEvent is one tick with a cloned chalkboard snapshot.
type DerivationEvent struct {
	FigaroLT uint64
	Snapshot chalkboard.Snapshot
}

// DurDerivDeps is per-aria construction state for Make/Resolve.
type DurDerivDeps struct {
	AriaID       string
	ProviderName string
	FigLog    store.Log[message.Message]
	Translator   store.Log[[]json.RawMessage]
}

// DurDerivReg registers a durable derivation.
// Alias is the CLI key (figaro -s <Alias>). Filename is relative to
// arias/<id>/. Resolve overrides Filename with a dynamic path.
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

// Register adds a derivation to the global registry.
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

// Registrations returns a copy of the registered derivations.
func Registrations() []DurDerivReg {
	regsMu.RLock()
	defer regsMu.RUnlock()
	out := make([]DurDerivReg, len(regs))
	copy(out, regs)
	return out
}

// LookupRegistration finds a registration by alias.
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

// AriaDir returns the per-aria directory, "" if not file-backed.
func AriaDir(backend store.Backend, ariaID string) string {
	fb, ok := backend.(interface{ Dir() string })
	if !ok {
		return ""
	}
	return filepath.Join(fb.Dir(), ariaID)
}

// DerivationFilePath returns the on-disk path for a derivation.
func DerivationFilePath(backend store.Backend, deps DurDerivDeps, reg DurDerivReg) string {
	dir := AriaDir(backend, deps.AriaID)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, reg.filenameFor(deps))
}

// derivationLoop is one goroutine per (figaro, registration).
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
			// Drain pending ticks before exit.
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
		slog.Warn("derivation ontick", "alias", l.alias, "err", err)
		return
	}
	if err := writeAtomic(l.path, buf.Bytes()); err != nil {
		// ENOENT = parent dir removed (test teardown, aria deletion).
		if !os.IsNotExist(err) {
			slog.Warn("derivation write", "alias", l.alias, "path", l.path, "err", err)
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

// derivedFanout owns all per-figaro derivation loops.
type derivedFanout struct {
	loops []*derivationLoop
}

// startDerived spins up one loop per registration.
func startDerived(
	ctx context.Context,
	ariaID, providerName string,
	backend store.Backend,
	figLog store.Log[message.Message],
	translator store.Log[[]json.RawMessage],
) *derivedFanout {
	dir := AriaDir(backend, ariaID)
	if dir == "" {
		return nil
	}
	deps := DurDerivDeps{
		AriaID:       ariaID,
		ProviderName: providerName,
		FigLog:    figLog,
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



func init() {
	Register(DurDerivReg{
		Alias:    "summary",
		Filename: "meta.json",
		Make: func(d DurDerivDeps) DurableDerivation {
			return &summaryDerivation{figLog: d.FigLog}
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
				figLog:    d.FigLog,
			}
		},
	})
	Register(DurDerivReg{
		Alias:    "meta",
		Filename: "derived/meta.json",
		Make: func(d DurDerivDeps) DurableDerivation {
			return &metaDerivation{
				ariaID:       d.AriaID,
				providerName: d.ProviderName,
				figLog:    d.FigLog,
			}
		},
	})
}

// summaryDerivation writes arias/<id>/meta.json.
type summaryDerivation struct {
	figLog store.Log[message.Message]
}

func (s *summaryDerivation) OnTick(w io.Writer, evt DerivationEvent) error {
	now := time.Now().UnixMilli()
	out := store.AriaMeta{LastActiveMS: now, LastFigaroLT: evt.FigaroLT}
	for _, e := range s.figLog.Read() {
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

// translatorDerivation writes per-provider cache stats.
type translatorDerivation struct {
	providerName string
	translator   store.Log[[]json.RawMessage]
}

func (t *translatorDerivation) OnTick(w io.Writer, evt DerivationEvent) error {
	out := store.TranslationMeta{Provider: t.providerName, LastUpdateMS: time.Now().UnixMilli()}
	if t.translator != nil {
		entries := t.translator.Read()
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

// usageDerivation writes arias/<id>/derived/usage.json.
type usageDerivation struct {
	ariaID       string
	providerName string
	figLog    store.Log[message.Message]
}

// Usage is the on-disk shape for usage.json.
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
	for _, e := range u.figLog.Read() {
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

// metaDerivation writes arias/<id>/derived/meta.json: a self-
// contained snapshot of every column the `figaro list` and
// `figaro status` views need. The angelus list handler reads this
// for dormant arias so the rendered table doesn't show "~0k" /
// blank model for figaros that aren't currently live.
//
// Source of truth: the IR log + the chalkboard snapshot we already
// receive each tick. Provider/Model are pulled from the snapshot
// (system.provider / system.model). ContextTokens uses the same
// estimator the live agent uses (tokens.ContextSize) so the dormant
// view matches what the user saw right before the figaro went idle.
type metaDerivation struct {
	ariaID       string
	providerName string
	figLog       store.Log[message.Message]
}

// MetaSnapshot is the on-disk shape for derived/meta.json. Field
// names mirror rpc.FigaroInfoResponse so the angelus handler can map
// directly without a translation layer.
type MetaSnapshot struct {
	AriaID           string `json:"aria_id"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	MessageCount     int    `json:"message_count"`
	TokensIn         int    `json:"tokens_in,omitempty"`
	TokensOut        int    `json:"tokens_out,omitempty"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	ContextTokens    int    `json:"context_tokens"`
	ContextExact     bool   `json:"context_exact"`
	LastFigaroLT     uint64 `json:"last_figaro_lt,omitempty"`
	LastUpdateMS     int64  `json:"last_update_ms"`
}

func (l *metaDerivation) OnTick(w io.Writer, evt DerivationEvent) error {
	out := MetaSnapshot{
		AriaID:       l.ariaID,
		Provider:     l.providerName,
		LastFigaroLT: evt.FigaroLT,
		LastUpdateMS: time.Now().UnixMilli(),
	}
	if p := evt.Snapshot.Lookup("system.provider"); p != nil && *p != "" {
		out.Provider = *p
	}
	if m := evt.Snapshot.Lookup("system.model"); m != nil {
		out.Model = *m
	}

	entries := l.figLog.Read()
	msgs := make([]message.Message, 0, len(entries))
	for _, e := range entries {
		out.MessageCount++
		m := e.Payload
		msgs = append(msgs, m)
		if m.Usage != nil {
			out.TokensIn += m.Usage.InputTokens
			out.TokensOut += m.Usage.OutputTokens
			out.CacheReadTokens += m.Usage.CacheReadTokens
			out.CacheWriteTokens += m.Usage.CacheWriteTokens
		}
	}
	out.ContextTokens, out.ContextExact = tokens.ContextSize(msgs)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
