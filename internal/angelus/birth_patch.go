package angelus

// How a birth patch is assembled -- outfit fold, runtime fill-ins, and the
// small typed readers over a form.Patch. Nothing here handles a request.
//
// Split out of protocol.go, which had grown to 2,011 lines and answered every
// question at once. Same package, same behaviour: only the reader's job
// changes. plans/api-coherence.md step 5.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// newOutfitter builds the daemon's ONE resolver, with its snapshot store in
// the runtime directory. Snapshots are what keep a resolution from straddling
// an edit: the first read of a file in an epoch pins its bytes, and everything
// derived in that epoch: including a fold rebuilt after eviction: is derived
// from the pinned copy. Runtime-scoped on purpose: the guarantee they provide
// is per-daemon-run, and a reboot has nothing to be consistent with.
func newOutfitter(a *Angelus, loaded *config.Loaded) *outfit.Outfitter {
	dir := ""
	if loaded != nil {
		dir = loaded.ConfigDir
	}
	snap := ""
	if a != nil && a.RuntimeDir != "" {
		snap = filepath.Join(a.RuntimeDir, "outfit-snapshots")
	}
	return outfit.NewAt(dir, snap)
}

// outfitVerLabel renders the version column: "live" when the stamped hash
// matches the current one, else the stamped hash's first 8 chars.
func outfitVerLabel(stamped, current, legacy string) string {
	if stamped == "" {
		return ""
	}
	if stamped == current || (legacy != "" && stamped == legacy) {
		return "live"
	}
	if len(stamped) > 8 {
		return stamped[:8]
	}
	return stamped
}

// ensureDefaultForm returns the id of the current default form, minting a
// fresh one when due. The lifecycle (v2 brief §6): `fig outfit reload`
// only sets a dirty flag; the compute happens HERE, on the next fig new -
// materialized files are hashed and compared against the record, and a
// pointer that is clean and whose node still exists is reused with NO
// comparison at all (the cheap path, and the prompt-cache-preserving one:
// reuse of the same node is what shares the rendered prefix). A remint is
// due when: no record, dirty + hash moved, or dirty + the form was patched
// by hand since birth (propagating an ad-hoc patch to every future aria is
// exactly what the dirty-compute refuses to do silently).
// birthParent is the node a fresh aria forks from: the shared default form
// when the caller named no outfit, and an outfit node for the named closure
// when it did.
func (h *handlers) birthParent(backend store.Backend, closure form.Patch, outfitName string, named bool) (string, error) {
	if !named {
		return h.ensureDefaultForm(backend, closure, outfitName)
	}
	id, err := backend.CreateOutfit(outfitName, birthPatch(closure, outfitName, ""))
	if err != nil {
		return "", fmt.Errorf("outfit node %q: %w", outfitName, err)
	}
	return id, nil
}

func (h *handlers) ensureDefaultForm(backend store.Backend, stumpPatch form.Patch, outfitName string) (string, error) {
	rec, err := backend.LoadDefaultForm()
	if err != nil {
		return "", fmt.Errorf("default form record: %w", err)
	}
	if rec != nil {
		if _, ok := backend.Node(rec.FormID); !ok {
			rec = nil // the form was removed; remint
		}
	}
	if rec != nil && !rec.Dirty {
		return rec.FormID, nil
	}
	birth := birthPatch(stumpPatch, outfitName, "")
	hash, err := store.ContentVersion(birth)
	if err != nil {
		return "", fmt.Errorf("default form hash: %w", err)
	}
	if rec != nil && rec.Dirty && rec.BirthHash == hash {
		if v, verr := backend.FormVersion(rec.FormID); verr == nil && v == rec.BirthVersion {
			rec.Dirty = false // same files, untouched form: reload is a no-op
			if err := backend.SaveDefaultForm(rec); err != nil {
				return "", err
			}
			return rec.FormID, nil
		}
	}
	id, version, err := backend.CreateForm("", birth)
	if err != nil {
		return "", fmt.Errorf("mint default form: %w", err)
	}
	if err := backend.SaveDefaultForm(&store.DefaultFormRecord{
		FormID: id, BirthHash: hash, BirthVersion: version,
		BundledRoot: outfit.BundledSkillsRoot(),
	}); err != nil {
		return "", err
	}
	slog.Info("default form minted", "form", id, "outfit", outfitName, "hash", hash)
	return id, nil
}

// outfitReload flags the default form for recomputation on the next
// `fig new`. Deliberately cheap: no files are read here, and there is NO
// inverse verb: outfit files are one-way sources of truth.
func (h *handlers) outfitReload(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// Turn the resolver's epoch over first: whatever else this verb decides,
	// asking for a reload means the files on disk are the truth now. It reads
	// nothing: the next fold does the reading: which is what keeps this the
	// cheap verb §6 of the brief says it is.
	if _, ofit := h.settings(); ofit != nil {
		ofit.Reload()
	}
	rec, err := h.angelus.Backend.LoadDefaultForm()
	if err != nil {
		return nil, err
	}
	if rec == nil {
		// Nothing minted yet: the next fig new computes from files anyway.
		return rpc.OutfitReloadResponse{}, nil
	}
	rec.Dirty = true
	if err := h.angelus.Backend.SaveDefaultForm(rec); err != nil {
		return nil, err
	}
	return rpc.OutfitReloadResponse{Flagged: true, FormID: rec.FormID}, nil
}

// runtimeFillins returns the per-process boot keys the outfit can't
// supply: the working dir (system.cwd/root), allowlisted env vars, and
// the aria id (non-system, so the agent can read it from a reminder and
// `figaro set --id <id> mantra …`).
// birthPatch is what an aria is born carrying: the materialized outfit, the name
// it answers to, and the content hash of both. The hash covers the patch minus
// itself: it cannot cover its own value, and the NAME is inside it, so two
// outfits with identical bodies and different names stay two identities.
// forkDress is what a branch is born carrying. The dressing may be empty, a
// plain `fig fork` asks for nothing: but the patch may not be: the child
// inherits its parent's form, aria_id included, and an aria that answers to its
// parent's id cannot fork itself. The re-stamp is the floor.
func forkDress(dress form.Patch, parent string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}, Remove: dress.Remove}
	for k, v := range dress.Set {
		p.Set[k] = v
	}
	// A placeholder the writer replaces would be a lie; the id is not known
	// until the child exists, so aria_id is re-stamped by the boot patch that
	// follows. What this guarantees is that the birth patch is never empty.
	p.Set[form.ForkedFromKey] = json.RawMessage(`"` + parent + `"`)
	return p
}

// mergePatches folds b over a.
func mergePatches(a, b form.Patch) form.Patch {
	out := form.Patch{Set: map[string]json.RawMessage{}, Remove: append(append([]string(nil), a.Remove...), b.Remove...)}
	for k, v := range a.Set {
		out.Set[k] = v
	}
	for k, v := range b.Set {
		out.Set[k] = v
	}
	return out
}

// childBirthPatch is what an aria writes for ITSELF: the dressing it asked for
// and the runtime fill-ins. Everything else it inherits from the stump. It is
// never empty: cwd is always known: which is what lets ForkWith demand a
// patch.
func childBirthPatch(dress form.Patch, cwd string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}, Remove: dress.Remove}
	for k, v := range dress.Set {
		p.Set[k] = v
	}
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		p.Set["system.cwd"] = b
	}
	return p
}

func birthPatch(outfitPatch form.Patch, outfitName, cwd string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}, Remove: outfitPatch.Remove}
	for k, v := range outfitPatch.Set {
		p.Set[k] = v
	}
	if b, err := json.Marshal(outfitName); err == nil && outfitName != "" {
		p.Set["system.outfit_name"] = b
	}
	if ver, err := store.ContentVersion(p); err == nil {
		if b, mErr := json.Marshal(ver); mErr == nil {
			p.Set["system.outfit_version"] = b
		}
	}
	// cwd rides the birth patch so the very first turn resolves tools against
	// the right directory; aria_id cannot, because the id does not exist yet.
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		p.Set["system.cwd"] = b
	}
	return p
}

func birthCwd(requested string) string {
	if isDir(requested) {
		return requested
	}
	if requested != "" {
		slog.Error("birth cwd unusable, falling back to the daemon's", "cwd", requested)
	}
	dir, _ := os.Getwd()
	return dir
}

func isDir(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func runtimeFillins(ariaID, cwd string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}}
	if b, err := json.Marshal(ariaID); err == nil && ariaID != "" {
		p.Set["aria_id"] = b
	}
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		p.Set["system.cwd"] = b
	}
	if env := form.EnvironmentPatch(); !env.IsEmpty() {
		for k, v := range env.Set {
			p.Set[k] = v
		}
	}
	return p
}

// convBootPatch is the conversation's boot transition: the runtime fill-ins,
// and nothing else. What the caller asked for is already in the birth patch -
// inherited through the fork watermark and rendered once in the shared prefix.
func convBootPatch(ariaID, cwd string) form.Patch {
	return runtimeFillins(ariaID, cwd)
}

// withAriaID returns p with aria_id set (used once the ephemeral id is
// minted).
func withAriaID(p form.Patch, ariaID string) form.Patch {
	if b, err := json.Marshal(ariaID); err == nil {
		if p.Set == nil {
			p.Set = map[string]json.RawMessage{}
		}
		p.Set["aria_id"] = b
	}
	return p
}

// patchString reads a string value from a form.Patch's Set map.
func patchString(p form.Patch, key string) string {
	raw, ok := p.Set[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// patchInt reads an int value from a form.Patch's Set map.
func patchInt(p form.Patch, key string) int {
	raw, ok := p.Set[key]
	if !ok {
		return 0
	}
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}

// patchBool reads a bool value from a form.Patch's Set map.
func patchBool(p form.Patch, key string) bool {
	raw, ok := p.Set[key]
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

// knobsFromPatch extracts the operational provider knobs from a
// outfit patch's system.* keys.
func knobsFromPatch(p form.Patch) providerPkg.Knobs {
	return providerPkg.Knobs{
		Model:            patchString(p, "system.model"),
		MaxTokens:        patchInt(p, "system.max_tokens"),
		ReminderRenderer: patchString(p, "system.reminder_renderer"),
		UseOfficialSDK:   patchBool(p, "system.use_official_sdk"),
	}
}
