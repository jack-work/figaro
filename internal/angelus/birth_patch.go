package angelus

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

// newOutfitter builds the daemon's one resolver. Snapshots live in the
// runtime dir: they pin a file's bytes for an epoch so a fold never
// straddles an edit, and the guarantee is per daemon run.
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

// birthParent is the shared default form when the caller named no outfit,
// else an outfit node for the named closure.
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

// ensureDefaultForm reuses a clean record without comparison: reusing the
// same node is what shares the rendered prompt prefix. A dirty record is
// re-hashed and reminted only when the files changed.
func (h *handlers) ensureDefaultForm(backend store.Backend, stumpPatch form.Patch, outfitName string) (string, error) {
	rec, err := backend.LoadDefaultForm()
	if err != nil {
		return "", fmt.Errorf("default form record: %w", err)
	}
	if rec != nil {
		if _, ok := backend.Node(rec.FormID); !ok {
			rec = nil
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
			rec.Dirty = false
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

// outfitReload flags the default form for recomputation on the next `fig new`; it reads no files.
func (h *handlers) outfitReload(ctx context.Context, params json.RawMessage) (interface{}, error) {
	if _, ofit := h.settings(); ofit != nil {
		ofit.Reload()
	}
	rec, err := h.angelus.Backend.LoadDefaultForm()
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return rpc.OutfitReloadResponse{}, nil
	}
	rec.Dirty = true
	if err := h.angelus.Backend.SaveDefaultForm(rec); err != nil {
		return nil, err
	}
	return rpc.OutfitReloadResponse{Flagged: true, FormID: rec.FormID}, nil
}

// forkDress guarantees a non-empty birth patch; aria_id is re-stamped by
// the boot patch once the child exists.
func forkDress(dress form.Patch, parent string) form.Patch {
	return form.Merge(dress, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		form.ForkedFromKey: json.RawMessage(`"` + parent + `"`),
	}, nil))
}

func mergePatches(a, b form.Patch) form.Patch {
	return form.Merge(a, b)
}

// childBirthPatch is never empty (cwd is always known), which lets ForkWith demand a patch.
func childBirthPatch(dress form.Patch, cwd string) form.Patch {
	add := map[string]json.RawMessage{}
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		add["system.cwd"] = b
	}
	return form.Merge(dress, form.Build(form.Snapshot{}, add, nil))
}

// birthPatch hashes the outfit with its name inside, so two outfits with
// identical bodies and different names stay two identities.
func birthPatch(outfitPatch form.Patch, outfitName, cwd string) form.Patch {
	named := map[string]json.RawMessage{}
	if b, err := json.Marshal(outfitName); err == nil && outfitName != "" {
		named["system.outfit_name"] = b
	}
	p := form.Merge(outfitPatch, form.Build(form.Snapshot{}, named, nil))

	// The version covers the named patch, so it is stamped after it.
	add := map[string]json.RawMessage{}
	if ver, err := store.ContentVersion(p); err == nil {
		if b, mErr := json.Marshal(ver); mErr == nil {
			add["system.outfit_version"] = b
		}
	}
	// aria_id cannot ride the birth patch: the id does not exist yet.
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		add["system.cwd"] = b
	}
	return form.Merge(p, form.Build(form.Snapshot{}, add, nil))
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
	fill := map[string]json.RawMessage{}
	if b, err := json.Marshal(ariaID); err == nil && ariaID != "" {
		fill["aria_id"] = b
	}
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		fill["system.cwd"] = b
	}
	return form.Merge(form.Build(form.Snapshot{}, fill, nil), form.EnvironmentPatch())
}

// convBootPatch carries only the runtime fill-ins; the caller's patch is
// already in the birth patch and rendered once in the shared prefix.
func convBootPatch(ariaID, cwd string) form.Patch {
	return runtimeFillins(ariaID, cwd)
}

func withAriaID(p form.Patch, ariaID string) form.Patch {
	b, err := json.Marshal(ariaID)
	if err != nil {
		return p
	}
	return form.Merge(p, form.Build(form.Snapshot{}, map[string]json.RawMessage{"aria_id": b}, nil))
}

func patchString(p form.Patch, key string) string {
	e, ok := p.Entry(key)
	if !ok || e.IsRemoval() {
		return ""
	}
	raw := e.New
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func patchInt(p form.Patch, key string) int {
	e, ok := p.Entry(key)
	if !ok || e.IsRemoval() {
		return 0
	}
	raw := e.New
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}

func knobsFromPatch(p form.Patch) providerPkg.Knobs {
	return providerPkg.Knobs{
		Model:            patchString(p, "system.model"),
		MaxTokens:        patchInt(p, "system.max_tokens"),
		ReminderRenderer: patchString(p, "system.reminder_renderer"),
	}
}
