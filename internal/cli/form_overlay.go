package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

type overlayLeaf struct {
	id         string
	base       json.RawMessage
	overridden bool
}

type overlayMerge struct {
	view   *rpc.FormView
	from   form.Snapshot
	base   form.Stamp
	over   form.Stamp
	snap   form.Snapshot
	rows   map[string]bool
	leaves map[string]overlayLeaf
	labels map[string]string
}

type overlayOpener func(id string, changed func()) (*formView, func(), <-chan struct{}, error)

func overlayRows(hint *rpc.FormView, base form.Snapshot) map[string]string {
	rows := map[string]string{}
	for k, raw := range base.All() {
		segs := strings.Split(k, ".")
		if hint.RowAria != "" {
			if len(segs) < 2 || segs[len(segs)-1] != hint.RowAria {
				continue
			}
			if _, ok := rpc.MatchPath(hint.Rows, segs[:len(segs)-1]); !ok {
				continue
			}
			var id string
			if json.Unmarshal(raw, &id) == nil && id != "" {
				rows[strings.Join(segs[:len(segs)-1], ".")] = id
			}
			continue
		}
		for i := 1; i < len(segs); i++ {
			if _, ok := rpc.MatchPath(hint.Rows, segs[:i]); ok {
				rows[strings.Join(segs[:i], ".")] = segs[i-1]
			}
		}
	}
	return rows
}

func mergeOverlay(hint *rpc.FormView, base, over form.Snapshot) *overlayMerge {
	m := &overlayMerge{view: hint, from: over, base: base.RootNode().Stamp(), over: over.RootNode().Stamp(), snap: base,
		rows: map[string]bool{}, leaves: map[string]overlayLeaf{}, labels: map[string]string{}}
	key := hint.Overlay.Key
	for row, id := range overlayRows(hint, base) {
		m.labels[id] = row[strings.LastIndexByte(row, '.')+1:]
		leaf := row + "." + key
		baseRaw, hasBase := base.Get(leaf)
		overRaw, overridden := over.Get(id)
		if !overridden && !hasBase {
			continue
		}
		if overridden {
			m.snap = m.snap.SetPath(leaf, overRaw)
			m.rows[row] = true
		}
		m.leaves[leaf] = overlayLeaf{id: id, base: baseRaw, overridden: overridden}
	}
	return m
}

func (m *overlayMerge) baseText(l overlayLeaf) string {
	if l.base == nil {
		return "base: none"
	}
	var s string
	if json.Unmarshal(l.base, &s) == nil {
		if label, ok := m.labels[s]; ok {
			return "base " + label
		}
		return "base " + s
	}
	return "base " + string(l.base)
}

func (m *overlayMerge) note(path, note string) string {
	if m == nil {
		return note
	}
	if m.rows[path] {
		if note == "" {
			return "override"
		}
		return note + " override"
	}
	if l, ok := m.leaves[path]; ok && l.overridden {
		return m.baseText(l)
	}
	return note
}

func (m *overlayMerge) hint(path string) string {
	if m == nil {
		return ""
	}
	l, ok := m.leaves[path]
	switch {
	case !ok:
		return ""
	case l.overridden:
		return "c override · x clear"
	}
	return "c override"
}

func (v *formView) overlaid(base form.Snapshot) (form.Snapshot, *overlayMerge) {
	v.mu.Lock()
	hint, ov, cached := v.view, v.overlay, v.merge
	v.mu.Unlock()
	if hint == nil || hint.Overlay == nil {
		return base, nil
	}
	var over form.Snapshot
	if ov != nil {
		over = ov.source().Shown()
	}
	if cached != nil && cached.view == hint && cached.base == base.RootNode().Stamp() && cached.over == over.RootNode().Stamp() {
		return cached.snap, cached
	}
	m := mergeOverlay(hint, base, over)
	v.mu.Lock()
	v.merge = m
	v.mu.Unlock()
	return m.snap, m
}

func overlayFormID(base, overlay string) string {
	node, _ := rpc.SplitNode(base)
	return qualifiedIn(node, overlay)
}

func (v *formView) overlayLine(path string, clear bool) (string, bool) {
	v.mu.Lock()
	m, aria := v.merge, v.aria
	v.mu.Unlock()
	if m == nil {
		return "", false
	}
	l, ok := m.leaves[path]
	if !ok {
		return "", false
	}
	id := overlayFormID(aria, m.view.Overlay.Form)
	if clear {
		if !l.overridden {
			return "", false
		}
		return "form unset --id " + id + " " + shellQuote(l.id), true
	}
	raw, _ := m.snap.Get(path)
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		compact.Reset()
		compact.Write(raw)
	}
	return "form set --id " + id + " " + shellQuote(l.id) + " " + shellQuote(compact.String()), true
}

func (v *formView) hasOverlay() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.view != nil && v.view.Overlay != nil
}

func (v *formView) ensureOverlay() {
	v.mu.Lock()
	if v.view == nil || v.view.Overlay == nil || v.openOverlay == nil || v.overlayOn || v.closing {
		v.mu.Unlock()
		return
	}
	v.overlayOn = true
	id := overlayFormID(v.aria, v.view.Overlay.Form)
	open := v.openOverlay
	v.mu.Unlock()
	go v.dialOverlay(id, open)
}

func (v *formView) dialOverlay(id string, open overlayOpener) {
	ov, closeFn, done, err := open(id, v.changed)
	v.mu.Lock()
	if v.closing {
		v.mu.Unlock()
		if closeFn != nil {
			closeFn()
		}
		return
	}
	if err != nil {
		v.notice = "overrides from " + id + ": " + err.Error()
		v.overlayOn = false
		v.items = nil
		v.mu.Unlock()
		v.changed()
		return
	}
	v.overlay, v.overlayClose = ov, closeFn
	v.items = nil
	v.mu.Unlock()
	v.changed()
	if done == nil {
		return
	}
	<-done
	time.Sleep(mountRedial)
	v.mu.Lock()
	if v.overlay != ov {
		v.mu.Unlock()
		return
	}
	v.overlay, v.overlayClose, v.overlayOn = nil, nil, false
	v.mu.Unlock()
	closeFn()
	v.ensureOverlay()
}

func (v *formView) closeOverlay() {
	v.mu.Lock()
	v.closing = true
	closeFn := v.overlayClose
	v.overlay, v.overlayClose = nil, nil
	v.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
}
