package form

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

const RuntimeRoot = "runtime"

const (
	ForkedFromKey       = "runtime.forked_from"
	StudiesKey          = "runtime.studies"
	TombstoneKey        = "runtime.tombstone"
	CheckpointMarksKey  = "runtime.checkpoint.marks"
	SuccessionWarnedKey = "runtime.succession_warned"
	RetiredKey          = "runtime.retired"
	FailureReasonKey    = "runtime.failure.reason"
	FailureCategoryKey  = "runtime.failure.category"
	FailureTurnKey      = "runtime.failure.turn"
	FailureAtKey        = "runtime.failure.at"
	ReapedKey           = "runtime.reaped"
	ReapedUntilKey      = "runtime.reaped_until"
	ReapedByKey         = "runtime.reaped_by"
	OwnerMadeKey        = "runtime.owner_made"
	RouteKey            = "runtime.route"
	ContextLimitKey     = "runtime.context.limit"
)

const RetireRequestKey = "system.retired"

type RuntimeRename struct{ Old, New string }

var runtimeRenames = []RuntimeRename{
	{"system.forked_from", ForkedFromKey},
	{"system.studies", StudiesKey},
	{"system.tombstone", TombstoneKey},
	{"system.checkpoint.marks", CheckpointMarksKey},
	{"system.succession_warned", SuccessionWarnedKey},
	{RetireRequestKey, RetiredKey},
	{"system.failure.reason", FailureReasonKey},
	{"system.failure.category", FailureCategoryKey},
	{"system.failure.turn", FailureTurnKey},
	{"system.failure.at", FailureAtKey},
	{"system.reaped", ReapedKey},
	{"system.reaped_until", ReapedUntilKey},
	{"system.reaped_by", ReapedByKey},
}

var legacyOf = func() map[string]string {
	m := make(map[string]string, len(runtimeRenames))
	for _, r := range runtimeRenames {
		m[r.New] = r.Old
	}
	return m
}()

func RuntimeRenames() []RuntimeRename { return slices.Clone(runtimeRenames) }

func LegacyName(key string) (string, bool) {
	old, ok := legacyOf[key]
	return old, ok
}

func HarnessKey(key string) bool {
	return strings.HasPrefix(key, "system.") || strings.HasPrefix(key, RuntimeRoot+".") ||
		key == "system" || key == RuntimeRoot
}

type KeyGetter interface {
	Get(string) (json.RawMessage, bool)
}

func RuntimeGet(s KeyGetter, key string) (json.RawMessage, bool) {
	if raw, ok := s.Get(key); ok {
		return raw, true
	}
	if old, ok := legacyOf[key]; ok {
		return s.Get(old)
	}
	return nil, false
}

func RuntimeHas(s Snapshot, key string) bool {
	_, ok := RuntimeGet(s, key)
	return ok
}

func RuntimeLookup(s Snapshot, key string) *string {
	if v := s.Lookup(key); v != nil {
		return v
	}
	if old, ok := legacyOf[key]; ok {
		return s.Lookup(old)
	}
	return nil
}

func RuntimeEntry(p Patch, key string) (Entry, bool) {
	e, ok := p.Entry(key)
	if ok && !e.IsRemoval() {
		return e, true
	}
	if old, legacy := legacyOf[key]; legacy {
		if le, lok := p.Entry(old); lok && (!ok || !le.IsRemoval()) {
			le.Key = key
			return le, true
		}
	}
	return e, ok
}

func RuntimeDrops(keys ...string) []string {
	out := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		out = append(out, k)
		if old, ok := legacyOf[k]; ok {
			out = append(out, old)
		}
	}
	return out
}

func MigrateRuntimeNames(s Snapshot) Patch {
	return renamePatch(s, func(r RuntimeRename) (string, string) { return r.Old, r.New })
}

func RevertRuntimeNames(s Snapshot) Patch {
	return renamePatch(s, func(r RuntimeRename) (string, string) { return r.New, r.Old })
}

func HoldsRuntimeLegacy(s Snapshot) bool {
	for _, r := range runtimeRenames {
		if s.Has(r.Old) {
			return true
		}
	}
	return false
}

func renamePatch(s Snapshot, dir func(RuntimeRename) (from, to string)) Patch {
	set := map[string]json.RawMessage{}
	var drop []string
	for _, r := range runtimeRenames {
		from, to := dir(r)
		raw, ok := s.Get(from)
		if !ok {
			continue
		}
		if _, taken := s.Get(to); !taken {
			set[to] = raw
		}
		drop = append(drop, from)
	}
	if len(drop) == 0 {
		return Patch{}
	}
	return pruneEmptyParents(s.Apply(Build(s, set, drop)), drop).Diff(s)
}

func pruneEmptyParents(s Snapshot, paths []string) Snapshot {
	var parents []string
	seen := map[string]bool{}
	for _, path := range paths {
		for i := strings.LastIndexByte(path, '.'); i > 0; i = strings.LastIndexByte(path[:i], '.') {
			if parent := path[:i]; !seen[parent] {
				seen[parent] = true
				parents = append(parents, parent)
			}
		}
	}
	slices.SortFunc(parents, func(a, b string) int { return strings.Count(b, ".") - strings.Count(a, ".") })
	for _, k := range parents {
		if raw, ok := s.Get(k); ok && bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
			s = s.DeletePath(k)
		}
	}
	return s
}

func RecordRetireRequest(s Snapshot, p Patch) Patch {
	e, ok := p.Entry(RetireRequestKey)
	if !ok {
		return p
	}
	after := s.Apply(p)
	if e.IsRemoval() {
		if !after.Has(RetiredKey) {
			return p
		}
		return Merge(p, Build(after, nil, []string{RetiredKey}))
	}
	return Merge(p, Build(after, map[string]json.RawMessage{RetiredKey: e.New}, []string{RetireRequestKey}))
}

func RuntimeSet(base Snapshot, set map[string]json.RawMessage) Patch {
	var drop []string
	for k := range set {
		if old, ok := legacyOf[k]; ok && base.Has(old) {
			drop = append(drop, old)
		}
	}
	return Build(base, set, drop)
}
