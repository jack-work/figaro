package form

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Patch is a change between two values; exactly one shape is non-nil and the
// zero Patch is identity. Each operation carries the value it displaces, so
// Inverse is a swap.
type Patch struct {
	Scalar *ScalarPatch `json:"scalar,omitempty"`
	Object *ObjectPatch `json:"object,omitempty"`
	List   *ListPatch   `json:"list,omitempty"`
}

type ScalarPatch struct {
	Before Value `json:"Before"`
	After  Value `json:"After"`
}

// ObjectPatch: Set creates; an existing key is altered through Update.
type ObjectPatch struct {
	Set    map[string]Value `json:"Set,omitempty"`
	Delete map[string]Value `json:"Delete,omitempty"`
	Update map[string]Patch `json:"Update,omitempty"`

	// New names the Update keys that did not exist: their leaves are
	// described rather than the subtree, so Inverse removes the parent
	// instead of leaving it empty.
	New map[string]bool `json:"New,omitempty"`
}

// ListPatch addresses items by key; position is Order's separate concern.
type ListPatch struct {
	Create []KeyedValue     `json:"Create,omitempty"`
	Delete []KeyedValue     `json:"Delete,omitempty"`
	Update map[string]Patch `json:"Update,omitempty"`
	Order  *Order           `json:"Order,omitempty"`
}

// KeyedValue.Key must be unique within the list.
type KeyedValue struct {
	Key   string `json:"Key"`
	Value Value  `json:"Value"`
}

// Order is a partial window onto the new order: the keys that moved plus one
// neighbour either side. Undeclared keys keep their position; Moved indexes
// into Declared.
type Order struct {
	Declared []string `json:"Declared"`
	Moved    []int    `json:"Moved"`

	// Prior is the same window in its source order.
	Prior      []string `json:"Prior,omitempty"`
	PriorMoved []int    `json:"PriorMoved,omitempty"`
}

func (o *Order) Reversed() *Order {
	if o == nil {
		return nil
	}
	if len(o.Prior) == 0 {
		return nil
	}
	return &Order{Declared: o.Prior, Moved: o.PriorMoved, Prior: o.Declared, PriorMoved: o.Moved}
}

func (p Patch) IsIdentity() bool {
	switch {
	case p.Scalar != nil:
		return p.Scalar.Before.Equal(p.Scalar.After)
	case p.Object != nil:
		o := p.Object
		if len(o.Set) > 0 || len(o.Delete) > 0 {
			return false
		}
		for _, c := range o.Update {
			if !c.IsIdentity() {
				return false
			}
		}
		return true
	case p.List != nil:
		l := p.List
		if len(l.Create) > 0 || len(l.Delete) > 0 || l.Order != nil {
			return false
		}
		for _, c := range l.Update {
			if !c.IsIdentity() {
				return false
			}
		}
		return true
	}
	return true
}

// Inverse: inverse(diff(A,B)) = diff(B,A).
func (p Patch) Inverse() Patch {
	switch {
	case p.Scalar != nil:
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.After, After: p.Scalar.Before}}
	case p.Object != nil:
		out := &ObjectPatch{Set: p.Object.Delete, Delete: p.Object.Set}
		if len(p.Object.Update) > 0 {
			out.Update = make(map[string]Patch, len(p.Object.Update))
			for k, c := range p.Object.Update {
				if p.Object.New[k] {
					if v, err := c.Apply(NewValue(json.RawMessage(`{}`))); err == nil {
						if out.Delete == nil {
							out.Delete = map[string]Value{}
						}
						out.Delete[k] = v
						continue
					}
				}
				out.Update[k] = c.Inverse()
			}
			if len(out.Update) == 0 {
				out.Update = nil
			}
		}
		return Patch{Object: out}
	case p.List != nil:
		out := &ListPatch{Create: p.List.Delete, Delete: p.List.Create}
		if len(p.List.Update) > 0 {
			out.Update = make(map[string]Patch, len(p.List.Update))
			for k, c := range p.List.Update {
				out.Update[k] = c.Inverse()
			}
		}
		out.Order = p.List.Order.Reversed()
		return Patch{List: out}
	}
	return Patch{}
}

// Apply: apply(diff(A,B), A) = B.
func (p Patch) Apply(v Value) (Value, error) {
	switch {
	case p.Scalar != nil:
		return p.Scalar.After, nil
	case p.Object != nil:
		return p.applyObject(v)
	case p.List != nil:
		return p.applyList(v)
	}
	return v, nil
}

func (p Patch) applyObject(v Value) (Value, error) {
	src, isObj := v.members()
	if !isObj && len(v.Raw()) > 0 && string(v.Raw()) != "null" {
		return Value{}, fmt.Errorf("object patch against a non-object")
	}
	// members() is a shared memo; copy before mutating.
	obj := make(map[string]Value, len(src)+len(p.Object.Set))
	for k, val := range src {
		obj[k] = val
	}
	for k, nv := range p.Object.Set {
		// An equal value keeps the stored bytes: providers cache on exact bytes.
		if cur, ok := obj[k]; ok && cur.Equal(nv) {
			continue
		}
		obj[k] = nv
	}
	for k := range p.Object.Delete {
		delete(obj, k)
	}
	for k, child := range p.Object.Update {
		cur, ok := obj[k]
		if !ok {
			cur = NewValue(json.RawMessage(`{}`))
		}
		next, err := child.Apply(cur)
		if err != nil {
			return Value{}, fmt.Errorf("%s: %w", k, err)
		}
		obj[k] = next
	}
	return encodeObject(obj), nil
}

func (p Patch) applyList(v Value) (Value, error) {
	items, err := decodeList(v)
	if err != nil {
		return Value{}, err
	}
	byKey := make(map[string]Value, len(items))
	order := make([]string, 0, len(items))
	for _, it := range items {
		byKey[it.Key] = it.Value
		order = append(order, it.Key)
	}
	for _, c := range p.List.Create {
		if _, exists := byKey[c.Key]; exists {
			return Value{}, fmt.Errorf("create of existing key %q", c.Key)
		}
		byKey[c.Key] = c.Value
		order = append(order, c.Key)
	}
	for _, d := range p.List.Delete {
		if _, ok := byKey[d.Key]; !ok {
			return Value{}, fmt.Errorf("delete of absent key %q", d.Key)
		}
		delete(byKey, d.Key)
		order = removeKey(order, d.Key)
	}
	for k, child := range p.List.Update {
		cur, ok := byKey[k]
		if !ok {
			return Value{}, fmt.Errorf("update of absent key %q", k)
		}
		next, err := child.Apply(cur)
		if err != nil {
			return Value{}, fmt.Errorf("%s: %w", k, err)
		}
		byKey[k] = next
	}
	if p.List.Order != nil {
		order = applyOrder(order, p.List.Order)
	}
	out := make([]KeyedValue, 0, len(order))
	for _, k := range order {
		out = append(out, KeyedValue{Key: k, Value: byKey[k]})
	}
	return encodeList(out)
}

// applyOrder fills the seats the declared keys occupied, in declared order;
// undeclared keys keep their position.
func applyOrder(cur []string, o *Order) []string {
	if o == nil || len(o.Declared) == 0 {
		return cur
	}
	declared := make(map[string]bool, len(o.Declared))
	for _, k := range o.Declared {
		declared[k] = true
	}
	seats := make([]int, 0, len(o.Declared))
	for i, k := range cur {
		if declared[k] {
			seats = append(seats, i)
		}
	}
	out := append([]string(nil), cur...)
	fill := 0
	for _, k := range o.Declared {
		if fill >= len(seats) {
			break
		}
		out[seats[fill]] = k
		fill++
	}
	return out
}

// Merge composes two patches applied in series:
// apply(merge(P,Q), A) = apply(Q, apply(P, A)).
// Order windows are relative to the post-edit sequence, so a list whose
// membership and order both change does not compose without the base.
func Merge(p, q Patch) Patch {
	switch {
	case p.IsIdentity():
		return q
	case q.IsIdentity():
		return p
	case p.Scalar != nil && q.Scalar != nil:
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.Before, After: q.Scalar.After}}
	case p.Object != nil && q.Object != nil:
		return Patch{Object: mergeObject(p.Object, q.Object)}
	case p.List != nil && q.List != nil:
		return Patch{List: mergeList(p.List, q.List)}
	}
	// Kinds disagree: the value changed shape between the steps.
	if p.Scalar != nil {
		after, err := q.Apply(p.Scalar.After)
		if err != nil {
			return q
		}
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.Before, After: after}}
	}
	if q.Scalar != nil {
		// Q.Before is the value after P; P inverted carries it back.
		before, err := p.Inverse().Apply(q.Scalar.Before)
		if err != nil {
			return q
		}
		return Patch{Scalar: &ScalarPatch{Before: before, After: q.Scalar.After}}
	}
	return q
}

func mergeObject(p, q *ObjectPatch) *ObjectPatch {
	out := &ObjectPatch{
		Set:    map[string]Value{},
		Delete: map[string]Value{},
		Update: map[string]Patch{},
	}
	for k, v := range p.Set {
		out.Set[k] = v
	}
	for k, v := range p.Delete {
		out.Delete[k] = v
	}
	for k, c := range p.Update {
		out.Update[k] = c
	}
	for k, v := range q.Set {
		if old, wasDeleted := out.Delete[k]; wasDeleted {
			delete(out.Delete, k)
			out.Update[k] = Patch{Scalar: &ScalarPatch{Before: old, After: v}}
			continue
		}
		out.Set[k] = v
	}
	for k, v := range q.Delete {
		if _, wasSet := out.Set[k]; wasSet {
			delete(out.Set, k)
			delete(out.Update, k)
			continue
		}
		delete(out.Update, k)
		out.Delete[k] = v
	}
	for k, c := range q.Update {
		if v, wasSet := out.Set[k]; wasSet {
			if next, err := c.Apply(v); err == nil {
				out.Set[k] = next
			}
			continue
		}
		if prior, ok := out.Update[k]; ok {
			out.Update[k] = Merge(prior, c)
			continue
		}
		out.Update[k] = c
	}
	return compactObject(out)
}

func mergeList(p, q *ListPatch) *ListPatch {
	out := &ListPatch{Update: map[string]Patch{}}

	// Create order is positional: Apply appends in array order.
	created := newKeyedSeq()
	deleted := newKeyedSeq()
	for _, c := range p.Create {
		created.put(c)
	}
	for _, d := range p.Delete {
		deleted.put(d)
	}
	for k, c := range p.Update {
		out.Update[k] = c
	}
	for _, c := range q.Create {
		created.put(c)
	}
	for _, d := range q.Delete {
		if _, wasCreated := created.get(d.Key); wasCreated {
			created.drop(d.Key)
			delete(out.Update, d.Key)
			continue
		}
		delete(out.Update, d.Key)
		deleted.put(d)
	}
	for k, c := range q.Update {
		if v, wasCreated := created.get(k); wasCreated {
			if next, err := c.Apply(v); err == nil {
				created.set(k, next)
			}
			continue
		}
		if prior, ok := out.Update[k]; ok {
			out.Update[k] = Merge(prior, c)
			continue
		}
		out.Update[k] = c
	}
	out.Create = created.items()
	out.Delete = deleted.items()
	out.Order = p.Order
	if q.Order != nil {
		out.Order = q.Order
	}
	return compactList(out)
}

type keyedSeq struct {
	order []string
	byKey map[string]Value
}

func newKeyedSeq() *keyedSeq { return &keyedSeq{byKey: map[string]Value{}} }

func (s *keyedSeq) put(kv KeyedValue) {
	if _, ok := s.byKey[kv.Key]; !ok {
		s.order = append(s.order, kv.Key)
	}
	s.byKey[kv.Key] = kv.Value
}

func (s *keyedSeq) set(key string, v Value) {
	if _, ok := s.byKey[key]; ok {
		s.byKey[key] = v
	}
}

func (s *keyedSeq) get(key string) (Value, bool) { v, ok := s.byKey[key]; return v, ok }

func (s *keyedSeq) drop(key string) {
	delete(s.byKey, key)
	s.order = removeKey(s.order, key)
}

func (s *keyedSeq) items() []KeyedValue {
	if len(s.order) == 0 {
		return nil
	}
	out := make([]KeyedValue, 0, len(s.order))
	for _, k := range s.order {
		out = append(out, KeyedValue{Key: k, Value: s.byKey[k]})
	}
	return out
}

func compactObject(o *ObjectPatch) *ObjectPatch {
	if len(o.Set) == 0 {
		o.Set = nil
	}
	if len(o.Delete) == 0 {
		o.Delete = nil
	}
	for k, c := range o.Update {
		if c.IsIdentity() {
			delete(o.Update, k)
		}
	}
	if len(o.Update) == 0 {
		o.Update = nil
	}
	return o
}

func compactList(l *ListPatch) *ListPatch {
	for k, c := range l.Update {
		if c.IsIdentity() {
			delete(l.Update, k)
		}
	}
	if len(l.Update) == 0 {
		l.Update = nil
	}
	return l
}

func removeKey(order []string, key string) []string {
	out := order[:0]
	for _, k := range order {
		if k != key {
			out = append(out, k)
		}
	}
	return out
}

// encodeObject writes the members' bytes verbatim.
func encodeObject(m map[string]Value) Value {
	raw := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		raw[k] = v.Raw()
	}
	return NewValue(encodeRawObject(raw))
}

// decodeList reads an array of {Key, Value}.
func decodeList(v Value) ([]KeyedValue, error) {
	if len(v.Raw()) == 0 {
		return nil, nil
	}
	var items []KeyedValue
	if err := json.Unmarshal(v.Raw(), &items); err != nil {
		return nil, fmt.Errorf("list patch against a non-list: %w", err)
	}
	return items, nil
}

func encodeList(items []KeyedValue) (Value, error) {
	b, err := json.Marshal(items)
	if err != nil {
		return Value{}, err
	}
	return NewValue(b), nil
}

func (p Patch) Entries() []Entry {
	var out []Entry
	var walk func(Patch, string)
	join := func(prefix, k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	walk = func(q Patch, prefix string) {
		switch {
		case q.Scalar != nil:
			if prefix != "" {
				out = append(out, Entry{Key: prefix, Old: q.Scalar.Before.Raw(), New: q.Scalar.After.Raw()})
			}
		case q.Object != nil:
			for k, v := range q.Object.Set {
				expand(v, join(prefix, k), &out)
			}
			for k, v := range q.Object.Delete {
				out = append(out, Entry{Key: join(prefix, k), Old: v.Raw()})
			}
			for k, c := range q.Object.Update {
				walk(c, join(prefix, k))
			}
		case q.List != nil:
			if prefix != "" {
				raw, err := json.Marshal(q.List.Create)
				if err == nil {
					out = append(out, Entry{Key: prefix, New: raw})
				}
			}
		}
	}
	walk(p, "")
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// expand yields a set value as its leaf paths.
func expand(v Value, prefix string, out *[]Entry) {
	obj, isObj := asObject(v)
	if !isObj || len(obj) == 0 {
		*out = append(*out, Entry{Key: prefix, New: v.Raw()})
		return
	}
	for k, child := range obj {
		expand(child, prefix+"."+k, out)
	}
}

// Entry is what the patch does at one path. A lookup of a key whose set value
// is an object answers with the whole value, where Entries would expand it.
func (p Patch) Entry(key string) (Entry, bool) {
	cur, rest := p, key
	for {
		if cur.Object == nil {
			break
		}
		seg, remainder, ok := longestPatchMember(cur.Object, rest)
		if !ok {
			break
		}
		if v, isSet := cur.Object.Set[seg]; isSet {
			if remainder == "" {
				return Entry{Key: key, New: v.Raw()}, true
			}
			if inner, ok := valueAt(v, remainder); ok {
				return Entry{Key: key, New: inner.Raw()}, true
			}
			return Entry{}, false
		}
		if v, isDel := cur.Object.Delete[seg]; isDel && remainder == "" {
			return Entry{Key: key, Old: v.Raw()}, true
		}
		child, isUpd := cur.Object.Update[seg]
		if !isUpd {
			break
		}
		if remainder == "" {
			if child.Scalar != nil {
				return Entry{Key: key, Old: child.Scalar.Before.Raw(), New: child.Scalar.After.Raw()}, true
			}
			if cur.Object.New[seg] {
				if v, err := child.Apply(NewValue(json.RawMessage(`{}`))); err == nil {
					return Entry{Key: key, New: v.Raw()}, true
				}
			}
			break
		}
		cur, rest = child, remainder
	}
	for _, e := range p.Entries() {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

func longestPatchMember(o *ObjectPatch, rest string) (seg, remainder string, ok bool) {
	has := func(name string) bool {
		if _, x := o.Set[name]; x {
			return true
		}
		if _, x := o.Delete[name]; x {
			return true
		}
		_, x := o.Update[name]
		return x
	}
	if has(rest) {
		return rest, "", true
	}
	best := ""
	for _, m := range []map[string]bool{namesOf(o)} {
		for name := range m {
			if len(name) < len(rest) && strings.HasPrefix(rest, name+".") && len(name) > len(best) {
				best = name
			}
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, rest[len(best)+1:], true
}

func namesOf(o *ObjectPatch) map[string]bool {
	out := map[string]bool{}
	for k := range o.Set {
		out[k] = true
	}
	for k := range o.Delete {
		out[k] = true
	}
	for k := range o.Update {
		out[k] = true
	}
	return out
}

// Build states what a form should hold and diffs the result.
func Build(base Snapshot, set map[string]json.RawMessage, remove []string) Patch {
	next := base
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		next = next.SetPath(k, set[k])
	}
	for _, k := range remove {
		next = next.DeletePath(k)
	}
	p := next.Diff(base)

	// A removal of a key the base lacks still has to appear in the patch.
	var missing []string
	for _, k := range remove {
		if _, ok := p.Entry(k); !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return p
	}
	sort.Strings(missing)
	for _, k := range missing {
		var old Value
		if raw, ok := base.Get(k); ok {
			old = NewValue(raw)
		}
		p = Merge(p, deleteAt(strings.Split(k, "."), old))
	}
	return p
}

func valueAt(v Value, path string) (Value, bool) {
	cur, rest := v, path
	for rest != "" {
		obj, ok := asObject(cur)
		if !ok {
			return Value{}, false
		}
		seg, remainder, found := longestMember(obj, rest)
		if !found {
			return Value{}, false
		}
		cur, rest = obj[seg], remainder
	}
	return cur, true
}

func deleteAt(segs []string, old Value) Patch {
	if len(segs) == 0 {
		return Patch{}
	}
	if len(segs) == 1 {
		return Patch{Object: &ObjectPatch{Delete: map[string]Value{segs[0]: old}}}
	}
	return Patch{Object: &ObjectPatch{
		Update: map[string]Patch{segs[0]: deleteAt(segs[1:], old)},
	}}
}
