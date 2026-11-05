package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/rpc"
)

const mountSep = "\x1f"

type mountOpener func(addr string) (*formView, actOn, func(), error)

type viewMount struct {
	addr  string
	node  string
	view  *formView
	send  actOn
	close func()
	note  string
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

func qualifiedIn(node, id string) string {
	if node == "" || id == "" {
		return id
	}
	return node + rpc.NodeSeparator + id
}

func (v *formView) mountRowsLocked(path, addr, indent string, width int) []pitRow {
	if v.opener == nil || addr == "" {
		return nil
	}
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
		return []pitRow{staticRow(clipLine(indent+m.note, width))}
	}
	inner := m.view.Items(max(width-displayWidth(indent), 20))
	out := make([]pitRow, 0, len(inner))
	for _, r := range inner {
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

func (v *formView) openMount(path string, m *viewMount) {
	view, send, closeFn, err := v.opener(m.addr)
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
		view.repaint, view.act, view.change = v.repaint, v.act, v.change
		m.view, m.send, m.close = view, send, closeFn
	}
	v.items = nil
	v.mu.Unlock()
	v.changed()
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

func (v *formView) forRow(id string) *formView {
	if m, _ := v.split(id); m != nil {
		return m.view
	}
	return v
}
