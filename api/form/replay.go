package form

import (
	"encoding/json"
	"strings"
)

const ReplayKey = "system.replay"

type Replay struct {
	Of     string `json:"of"`
	Turn   uint64 `json:"turn"`
	LT     uint64 `json:"lt"`
	AriaID string `json:"aria_id,omitempty"`
}

func ReadReplay(s Snapshot) (Replay, bool) {
	raw, ok := s.Get(ReplayKey)
	if !ok {
		return Replay{}, false
	}
	var r Replay
	if err := json.Unmarshal(raw, &r); err != nil || r.Of == "" || r.Turn == 0 {
		return Replay{}, false
	}
	return r, true
}

func (r Replay) Patch() Patch {
	b, err := json.Marshal(r)
	if err != nil {
		return Patch{}
	}
	return Build(Snapshot{}, map[string]json.RawMessage{ReplayKey: b}, nil)
}

func (r Replay) Mask(branch string, p Patch) Patch {
	p = rewriteLeaf(p, []string{"aria_id"}, func(v json.RawMessage) (json.RawMessage, bool) {
		if r.AriaID != "" && isString(v, branch) {
			return quote(r.AriaID), true
		}
		return v, true
	})
	return rewriteLeaf(p, strings.Split(ForkedFromKey, "."), func(v json.RawMessage) (json.RawMessage, bool) {
		return v, !isString(v, r.Of)
	})
}

func (r Replay) MaskSnapshot(branch string, s Snapshot) Snapshot {
	set := map[string]json.RawMessage{}
	var drop []string
	if v, ok := s.Get("aria_id"); ok && r.AriaID != "" && isString(v, branch) {
		set["aria_id"] = quote(r.AriaID)
	}
	if v, ok := s.Get(ForkedFromKey); ok && isString(v, r.Of) {
		drop = append(drop, ForkedFromKey)
	}
	if len(set) == 0 && len(drop) == 0 {
		return s
	}
	return s.Apply(Build(s, set, drop))
}

func isString(raw json.RawMessage, want string) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s == want
}

func quote(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func rewriteLeaf(p Patch, path []string, fn func(json.RawMessage) (json.RawMessage, bool)) Patch {
	if p.Object == nil || len(path) == 0 {
		return p
	}
	head, rest := path[0], path[1:]
	_, inSet := p.Object.Set[head]
	_, inUpdate := p.Object.Update[head]
	if !inSet && !inUpdate {
		return p
	}
	o := cloneObjectPatch(p.Object)
	if v, ok := o.Set[head]; ok {
		if nv, keep := rewriteValue(v.Raw(), rest, fn); keep {
			o.Set[head] = NewValue(nv)
		} else {
			delete(o.Set, head)
		}
	}
	if u, ok := o.Update[head]; ok {
		nu := u
		switch {
		case len(rest) == 0 && u.Scalar != nil:
			if nv, keep := fn(u.Scalar.After.Raw()); keep {
				nu = Patch{Scalar: &ScalarPatch{Before: u.Scalar.Before, After: NewValue(nv)}}
			} else {
				nu = Patch{}
			}
		case len(rest) > 0:
			nu = rewriteLeaf(u, rest, fn)
		}
		if nu.IsIdentity() {
			delete(o.Update, head)
			delete(o.New, head)
		} else {
			o.Update[head] = nu
		}
	}
	out := Patch{Object: o}
	if out.IsIdentity() {
		return Patch{}
	}
	return out
}

func rewriteValue(raw json.RawMessage, rest []string, fn func(json.RawMessage) (json.RawMessage, bool)) (json.RawMessage, bool) {
	if len(rest) == 0 {
		return fn(raw)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw, true
	}
	child, ok := obj[rest[0]]
	if !ok {
		return raw, true
	}
	if nv, keep := rewriteValue(child, rest[1:], fn); keep {
		obj[rest[0]] = nv
	} else {
		delete(obj, rest[0])
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return raw, true
	}
	return b, true
}

func cloneObjectPatch(o *ObjectPatch) *ObjectPatch {
	c := &ObjectPatch{}
	if o.Set != nil {
		c.Set = make(map[string]Value, len(o.Set))
		for k, v := range o.Set {
			c.Set[k] = v
		}
	}
	if o.Delete != nil {
		c.Delete = make(map[string]Value, len(o.Delete))
		for k, v := range o.Delete {
			c.Delete[k] = v
		}
	}
	if o.Update != nil {
		c.Update = make(map[string]Patch, len(o.Update))
		for k, v := range o.Update {
			c.Update[k] = v
		}
	}
	if o.New != nil {
		c.New = make(map[string]bool, len(o.New))
		for k, v := range o.New {
			c.New[k] = v
		}
	}
	return c
}
