package models

import (
	"encoding/json"
	"slices"

	"github.com/jack-work/figaro/api/form"
)

func RouteOf(bound form.Snapshot) []string {
	if raw, ok := bound.Get(form.RouteKey); ok {
		var r []string
		if json.Unmarshal(raw, &r) == nil && len(r) > 0 {
			return r
		}
	}
	return Route(Subject(bound))
}

func RecordRoute(s form.Snapshot, p form.Patch) form.Patch {
	if !touchesSubject(p) {
		return p
	}
	after := s.Apply(p)
	want := Route(Subject(after))
	var have []string
	if raw, ok := after.Get(form.RouteKey); ok {
		if json.Unmarshal(raw, &have) != nil {
			have = nil
		}
	}
	if slices.Equal(want, have) {
		return p
	}
	if want == nil {
		return form.Merge(p, form.Build(after, nil, []string{form.RouteKey}))
	}
	raw, err := json.Marshal(want)
	if err != nil {
		return p
	}
	return form.Merge(p, form.Build(after, map[string]json.RawMessage{form.RouteKey: raw}, nil))
}

func touchesSubject(p form.Patch) bool {
	for _, k := range []string{ProviderKey, ModelKey, form.RouteKey} {
		if _, ok := p.Entry(k); ok {
			return true
		}
	}
	e, ok := p.Entry("system")
	if !ok {
		return false
	}
	if e.IsRemoval() {
		return true
	}
	var sys map[string]json.RawMessage
	if json.Unmarshal(e.New, &sys) != nil {
		return true
	}
	_, hasProvider := sys["provider"]
	_, hasModel := sys["model"]
	return hasProvider || hasModel
}
