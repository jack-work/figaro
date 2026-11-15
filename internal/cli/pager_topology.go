package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

type pagerTopology struct {
	more   bool
	scope  string
	limit  int
	global bool
}

func topologyArgs(argv []string) (*pagerTopology, error) {
	o := &pagerTopology{}
	all, limited, home := false, false, false
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--more":
			o.more = true
		case a == "-H" || a == "--home":
			home = true
		case a == "-g" || a == "--global":
			o.global = true
		case a == "-a" || a == "--all":
			all = true
		case a == "-n" || a == "--limit" || strings.HasPrefix(a, "--limit="):
			value := strings.TrimPrefix(a, "--limit=")
			if value == a {
				i++
				if i == len(argv) {
					return nil, fmt.Errorf("ls: %s takes a positive count", a)
				}
				value = argv[i]
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("ls: %s takes a positive count", a)
			}
			o.limit, limited = n, true
		case strings.HasPrefix(a, "-"):
			return nil, fmt.Errorf("ls: unsupported pager flag %s", a)
		default:
			if o.scope != "" {
				return nil, fmt.Errorf("ls: give one scope")
			}
			o.scope = a
		}
	}
	if all && limited {
		return nil, fmt.Errorf("ls: -a/--all and -n are mutually exclusive")
	}
	if home && o.global {
		return nil, fmt.Errorf("ls: -H/--home and -g/--global are mutually exclusive")
	}
	if strings.HasPrefix(o.scope, "^") {
		if _, ok := parseClimb(o.scope); !ok {
			return nil, fmt.Errorf("ls: ^ climbs layers up (^, ^^, ^2)")
		}
	}
	return o, nil
}

func topologyFormPit(argv []string) (formPit, bool, error) {
	if len(argv) == 0 {
		return formPit{}, false, nil
	}
	if argv[0] == "ls" || argv[0] == "list" || argv[0] == "lsh" {
		o, err := topologyArgs(argv)
		p := formPit{name: "ls", form: rpc.TroupeFormID}
		p.ready = func(in *interactiveInput, v *formView) {
			v.topology = o
			seekAttended(v, in.mountForm)
		}
		return p, true, err
	}
	p, ok := formPitFor(argv[0])
	return p, ok && (len(argv) == 1 || !p.bare), nil
}

func (v *formView) topologySnapshot(snap form.Snapshot) form.Snapshot {
	o := v.topology
	if o == nil || v.view == nil || v.view.RowAria == "" || (o.scope == "" || o.scope == "/") && o.limit == 0 {
		return snap
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.projection != nil && v.projection.base == snap.RootNode().Stamp() {
		return v.projection.snap
	}
	root := newFormRoot(snap)
	s := &viewShape{hint: v.view, seeded: map[string]bool{}}
	var rows []*formNode
	var collect func(*formNode, []string)
	collect = func(n *formNode, labels []string) {
		for _, c := range s.order(labels, n.kids(), map[string]bool{}) {
			segs := append(labels[:len(labels):len(labels)], c.label)
			if _, ok := s.row(segs); ok {
				rows = append(rows, c)
			}
			if !c.leaf() {
				collect(c, segs)
			}
		}
	}
	collect(root, nil)
	scope := ""
	if o.scope != "" && o.scope != "/" {
		wanted := o.scope
		if strings.HasPrefix(wanted, "^") && v.subject != nil {
			if p := v.subject().Lookup("aria_id"); p != nil {
				wanted = *p
			}
		}
		for _, n := range rows {
			id := leafText(n, v.view.RowAria)
			if n.label == wanted || id == wanted || len(wanted) >= 5 && strings.HasPrefix(id, wanted) {
				scope = n.path
				break
			}
		}
		if strings.HasPrefix(o.scope, "^") && scope != "" {
			n, _ := parseClimb(o.scope)
			for ; n > 0; n-- {
				at := strings.LastIndex(scope, ".forks.")
				if at < 0 {
					break
				}
				scope = scope[:at]
			}
		}
		if scope == "" {
			v.notice = "ls: no figaro matching " + o.scope
		}
	}
	keep := map[string]bool{}
	rowPaths := map[string]bool{}
	for _, n := range rows {
		rowPaths[n.path] = true
	}
	count := 0
	for _, n := range rows {
		if o.scope != "" && o.scope != "/" && (scope == "" || n.path != scope && !strings.HasPrefix(n.path, scope+".")) {
			continue
		}
		if o.limit > 0 && count >= o.limit {
			break
		}
		keep[n.path] = true
		count++
	}
	out := map[string]json.RawMessage{}
	for key, raw := range snap.All() {
		owner := key
		for !rowPaths[owner] {
			at := strings.LastIndexByte(owner, '.')
			if at < 0 {
				owner = ""
				break
			}
			owner = owner[:at]
		}
		if owner == "" && o.more {
			out[key] = raw
			continue
		}
		if !keep[owner] {
			continue
		}
		if scope != "" {
			key = "arias." + scope[strings.LastIndexByte(scope, '.')+1:] + strings.TrimPrefix(key, scope)
		}
		out[key] = raw
	}
	projected := form.FromMap(out)
	v.projection = &topologyProjection{base: snap.RootNode().Stamp(), snap: projected}
	return projected
}

type topologyProjection struct {
	base form.Stamp
	snap form.Snapshot
}

func (v *formView) topologyRowOpen(id string) bool {
	if m, inner := v.split(id); m != nil {
		return m.view.topologyRowOpen(inner)
	}
	return v.isOpen(id)
}

func (v *formView) revealTopologyAria(id string, expanded bool) string {
	node, bare := rpc.SplitNode(id)
	if v.view != nil && v.view.RowAria != "" && node == v.node {
		at := v.revealRow(bare)
		if at != "" && expanded {
			v.mu.Lock()
			v.open[at] = true
			delete(v.through, at)
			v.openRev++
			v.mu.Unlock()
		}
		return at
	}
	v.mu.Lock()
	mounts := make(map[string]*formView, len(v.mounts))
	for path, m := range v.mounts {
		if m.view != nil {
			mounts[path] = m.view
		}
	}
	v.mu.Unlock()
	for path, inner := range mounts {
		if at := inner.revealTopologyAria(id, expanded); at != "" {
			return path + mountSep + at
		}
	}
	return ""
}
