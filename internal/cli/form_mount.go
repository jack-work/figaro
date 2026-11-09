package cli

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

const mountSep = "\x1f"

type mountOpener func(addr string) (*formView, actOn, func(), <-chan struct{}, error)

const (
	mountRedial      = time.Second
	mountOverlayWait = 2 * time.Second
)

type viewMount struct {
	addr  string
	node  string
	view  *formView
	send  actOn
	close func()
	note  string
	width int
	rev   uint64
}

type mountedText struct {
	indent string
	inner  pitRow
}

func (m mountedText) rowText() string { return m.indent + m.inner.str() }

func (m mountedText) rowDims() (int, int) {
	lead, tail := m.inner.dims()
	if tail > 0 {
		tail += len(m.indent)
	}
	return lead + len(m.indent), tail
}

func (v *formView) onNode(addr string) string {
	if strings.Contains(addr, rpc.NodeSeparator) {
		return addr
	}
	return qualifiedIn(v.node, addr)
}

func qualifiedIn(node, id string) string {
	if node == "" || id == "" {
		return id
	}
	return node + rpc.NodeSeparator + id
}

func (v *formView) mountRowsLocked(path, addr, cont string, width int) []pitRow {
	if v.opener == nil || addr == "" {
		return nil
	}
	addr = v.onNode(addr)
	if v.mounts == nil {
		v.mounts = map[string]*viewMount{}
	}
	m, ok := v.mounts[path]
	if ok && m.addr != addr {
		if m.close != nil {
			m.close()
		}
		ok = false
	}
	if !ok {
		node, _ := rpc.SplitNode(addr)
		m = &viewMount{addr: addr, node: node, note: "loading " + addr}
		v.mounts[path] = m
		go v.openMount(path, m)
	}
	if m.view == nil {
		return []pitRow{staticRow(clipLine(cont+"└─ "+m.note, width))}
	}
	m.width = max(width-displayWidth(cont)-3, 20)
	inner := m.view.Items(m.width)
	m.rev = m.view.Revision()
	return m.adopt(path, inner, mountIndents(inner, cont))
}

func (v *formView) mountsUnchangedLocked() bool {
	for _, m := range v.mounts {
		if m.view == nil {
			continue
		}
		if m.width == 0 {
			return false
		}
		m.view.Items(m.width)
		if m.view.Revision() != m.rev {
			return false
		}
	}
	return true
}

func (m *viewMount) adopt(path string, inner []pitRow, indents []string) []pitRow {
	out := make([]pitRow, 0, len(inner))
	for i, r := range inner {
		indent := indents[i]
		row := pitRow{textFrom: mountedText{indent: indent, inner: r}, yankFrom: r.yankFrom, yank: r.yank,
			note: r.note, hint: r.hint, tone: r.tone}
		for _, at := range r.marks {
			row.marks = append(row.marks, at+len(indent))
		}
		if r.id != "" {
			row.id = path + mountSep + r.id
		}
		if r.aria != "" && !strings.Contains(r.aria, rpc.NodeSeparator) {
			row.aria = qualifiedIn(m.node, r.aria)
			if r.yankFrom == nil {
				row.yank = row.aria
			}
		}
		out = append(out, row)
	}
	return out
}

func mountIndents(inner []pitRow, cont string) []string {
	tops := make([]bool, len(inner))
	last, nodes := -1, false
	for i, r := range inner {
		n, ok := r.textFrom.(*formNode)
		nodes = nodes || ok
		tops[i] = ok && n.guide == "" || !nodes
		if tops[i] {
			last = i
		}
	}
	out := make([]string, len(inner))
	under := cont + "│  "
	for i := range inner {
		switch {
		case tops[i] && i == last:
			out[i], under = cont+"└─ ", cont+"   "
		case tops[i]:
			out[i], under = cont+"├─ ", cont+"│  "
		default:
			out[i] = under
		}
	}
	return out
}

func (v *formView) openMount(path string, m *viewMount) {
	view, send, closeFn, done, err := v.opener(m.addr)
	v.mu.Lock()
	if v.mounts[path] != m {
		v.mu.Unlock()
		if closeFn != nil {
			closeFn()
		}
		return
	}
	if err != nil {
		m.note = err.Error()
	} else {
		view.repaint = func() { v.changed() }
		view.act = func(a rpc.ViewAction) {
			if act := v.act; act != nil {
				act(a)
			}
		}
		view.change = func() {
			if change := v.change; change != nil {
				change()
			}
		}
		view.clear = func() bool {
			if clear := v.clear; clear != nil {
				return clear()
			}
			return false
		}
		if m.node == "" {
			view.subject = v.subject
		}
		view.opener, view.node = v.opener, m.node
		m.view, m.send, m.close = view, send, closeFn
	}
	seek := err == nil && v.seeking == path
	if seek {
		v.seeking = ""
	}
	focus := v.focus
	v.items = nil
	v.mu.Unlock()
	if seek && focus != nil {
		if at := view.revealOpen(); at != "" {
			focus(path + mountSep + at)
		}
	}
	v.changed()
	if err == nil && done != nil {
		go v.redialWhenDone(path, m, done)
	}
}

func (v *formView) loadMount(path string) {
	v.mu.Lock()
	key := ""
	if v.opener != nil && v.view != nil {
		key = v.view.Mount
	}
	v.mu.Unlock()
	if key == "" || path == "" {
		return
	}
	raw, ok := v.source().Shown().Get(path + "." + key)
	var addr string
	if !ok || json.Unmarshal(raw, &addr) != nil || addr == "" {
		return
	}
	addr = v.onNode(addr)
	node, _ := rpc.SplitNode(addr)
	m := &viewMount{addr: addr, node: node, note: "loading " + addr}
	v.mu.Lock()
	if v.mounts == nil {
		v.mounts = map[string]*viewMount{}
	}
	v.mounts[path] = m
	v.mu.Unlock()
	v.openMount(path, m)
	v.mu.Lock()
	view := m.view
	v.mu.Unlock()
	if view != nil {
		view.awaitOverlay(mountOverlayWait)
	}
}

func (v *formView) redialWhenDone(path string, m *viewMount, done <-chan struct{}) {
	<-done
	time.Sleep(mountRedial)
	v.mu.Lock()
	if v.mounts[path] != m {
		v.mu.Unlock()
		return
	}
	fresh := &viewMount{addr: m.addr, node: m.node, note: "reconnecting to " + m.addr}
	v.mounts[path] = fresh
	v.items = nil
	v.mu.Unlock()
	if m.close != nil {
		m.close()
	}
	v.changed()
	v.openMount(path, fresh)
}

func (v *formView) dropMountsLocked(pointers map[string]bool) {
	for path, m := range v.mounts {
		if pointers[path] {
			continue
		}
		delete(v.mounts, path)
		if m.close != nil {
			go m.close()
		}
	}
}

func (v *formView) closeMounts() {
	v.mu.Lock()
	mounts := v.mounts
	v.mounts = nil
	v.mu.Unlock()
	for _, m := range mounts {
		if m.close != nil {
			m.close()
		}
	}
}

func (v *formView) split(id string) (*viewMount, string) {
	i := strings.Index(id, mountSep)
	if i < 0 {
		return nil, id
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	m := v.mounts[id[:i]]
	if m == nil || m.view == nil {
		return nil, id
	}
	return m, id[i+len(mountSep):]
}

func (v *formView) mountedView(path string) *formView {
	v.mu.Lock()
	defer v.mu.Unlock()
	if m := v.mounts[path]; m != nil {
		return m.view
	}
	return nil
}

func (v *formView) forRow(id string) *formView {
	if m, _ := v.split(id); m != nil {
		return m.view
	}
	return v
}
