package form

import (
	"encoding/json"
	"fmt"
	"sort"
)

// A patch is a change between two values. It describes its own shape, so a
// reader dispatches on which field is set rather than consulting a schema.
//
//	{Before, After}                  a scalar
//	{Set, Delete, Update}            an object
//	{Create, Delete, Update, Order}  a keyed list
//
// Each operation carries the value it displaces, so Inverse is a swap.
//
// Exactly one shape is non-nil. The zero Patch is Identity.
type Patch struct {
	Scalar *ScalarPatch `json:"scalar,omitempty"`
	Object *ObjectPatch `json:"object,omitempty"`
	List   *ListPatch   `json:"list,omitempty"`
}

// ScalarPatch is a whole-value swap.
type ScalarPatch struct {
	Before Value `json:"Before"`
	After  Value `json:"After"`
}

// ObjectPatch changes the keys of an object. Set creates; an existing key is
// altered through Update, which recurses to a ScalarPatch carrying Before.
type ObjectPatch struct {
	Set    map[string]Value `json:"Set,omitempty"`
	Delete map[string]Value `json:"Delete,omitempty"`
	Update map[string]Patch `json:"Update,omitempty"`
}

// ListPatch changes a keyed, ordered collection. Items are addressed by key;
// position is Order's separate concern.
type ListPatch struct {
	Create []KeyedValue     `json:"Create,omitempty"`
	Delete []KeyedValue     `json:"Delete,omitempty"`
	Update map[string]Patch `json:"Update,omitempty"`
	Order  *Order           `json:"Order,omitempty"`
}

// KeyedValue is a list item and its key. The key is supplied by whoever
// creates the item and must be unique within the list.
type KeyedValue struct {
	Key   string `json:"Key"`
	Value Value  `json:"Value"`
}

// Order is a partial window onto the new order: the keys that moved plus one
// neighbour either side as an anchor. Undeclared keys keep their position.
//
// Moved indexes into Declared and names the keys that changed position.
type Order struct {
	Declared []string `json:"Declared"`
	Moved    []int    `json:"Moved"`

	// Prior is the same window in its source order, so Inverse is a swap.
	Prior      []string `json:"Prior,omitempty"`
	PriorMoved []int    `json:"PriorMoved,omitempty"`
}

// Reversed is this window as it applies to the inverse patch.
func (o *Order) Reversed() *Order {
	if o == nil {
		return nil
	}
	if len(o.Prior) == 0 {
		return nil
	}
	return &Order{Declared: o.Prior, Moved: o.PriorMoved, Prior: o.Declared, PriorMoved: o.Moved}
}

// IsIdentity reports whether the patch changes nothing: diff(A, A).
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

// Inverse is the patch that undoes this one: inverse(diff(A,B)) = diff(B,A).
//
// It is a swap at every level and needs nothing but the patch itself, which is
func (p Patch) Inverse() Patch {
	switch {
	case p.Scalar != nil:
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.After, After: p.Scalar.Before}}
	case p.Object != nil:
		out := &ObjectPatch{Set: p.Object.Delete, Delete: p.Object.Set}
		if len(p.Object.Update) > 0 {
			out.Update = make(map[string]Patch, len(p.Object.Update))
			for k, c := range p.Object.Update {
				out.Update[k] = c.Inverse()
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

// Apply transforms v by this patch: apply(diff(A,B), A) = B.
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
	obj, err := decodeObject(v)
	if err != nil {
		return Value{}, err
	}
	for k, nv := range p.Object.Set {
		obj[k] = nv
	}
	for k := range p.Object.Delete {
		delete(obj, k)
	}
	for k, child := range p.Object.Update {
		cur, ok := obj[k]
		if !ok {
			return Value{}, fmt.Errorf("update of absent key %q", k)
		}
		next, err := child.Apply(cur)
		if err != nil {
			return Value{}, fmt.Errorf("%s: %w", k, err)
		}
		obj[k] = next
	}
	return encodeObject(obj)
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

// applyOrder reconciles a partial window against the order already in hand.
//
// The declared keys take the positions the declaration implies; everything
// undeclared keeps its original position. The seats are the union of the
// positions those keys occupied, filled in declared order.
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
func Merge(p, q Patch) Patch {
	switch {
	case p.IsIdentity():
		return q
	case q.IsIdentity():
		return p
	case p.Scalar != nil && q.Scalar != nil:
		// The pair spans both steps: what P found, what Q left.
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.Before, After: q.Scalar.After}}
	case p.Object != nil && q.Object != nil:
		return Patch{Object: mergeObject(p.Object, q.Object)}
	case p.List != nil && q.List != nil:
		return Patch{List: mergeList(p.List, q.List)}
	}
	// Kinds disagree, which means the value changed shape between the two
	// STEPS. Neither patch alone spans that, but together they do, and the
	// bridge is invertibility.
	if p.Scalar != nil {
		// P replaced the whole value, so what Q edits is P.After -- a
		// complete value we hold. Run Q against it.
		after, err := q.Apply(p.Scalar.After)
		if err != nil {
			return q
		}
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.Before, After: after}}
	}
	if q.Scalar != nil {
		// Q replaced the whole value, so the result is Q.After. The original
		// is not in either patch -- but Q.Before is the value AFTER P, and
		// P inverted carries it back.
		before, err := p.Inverse().Apply(q.Scalar.Before)
		if err != nil {
			return q
		}
		return Patch{Scalar: &ScalarPatch{Before: before, After: q.Scalar.After}}
	}
	// An object patch cannot feed a list patch: a value is not both. Only
	// reachable from a hand-built pair, and the later one is the honest
	// answer.
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
			// P removed it and Q put one back: a change, not a create, and
			// the value P destroyed is what it changed FROM.
			delete(out.Delete, k)
			out.Update[k] = Patch{Scalar: &ScalarPatch{Before: old, After: v}}
			continue
		}
		out.Set[k] = v
	}
	for k, v := range q.Delete {
		if _, wasSet := out.Set[k]; wasSet {
			// P created it and Q removed it: neither happened.
			delete(out.Set, k)
			delete(out.Update, k)
			continue
		}
		delete(out.Update, k)
		out.Delete[k] = v
	}
	for k, c := range q.Update {
		if v, wasSet := out.Set[k]; wasSet {
			// P created it and Q edited it: still a create, of the later value.
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

	// Creates keep their order. Apply appends them in array order, so the
	// array IS positional information: sorting it for tidiness silently
	// reorders the list a merged patch produces.
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
		// position: the item leaves its seat and is appended. Collapsing the
		// no position and do collapse -- see mergeObject.
		created.put(c)
	}
	for _, d := range q.Delete {
		if _, wasCreated := created.get(d.Key); wasCreated {
			// P created it and Q removed it: neither happened.
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

// ---- value shapes -------------------------------------------------------

// decodeObject reads a value as a JSON object of raw members. A patch that
// says "object" and meets something else is a bug in the differ, not a
// condition to paper over.
func decodeObject(v Value) (map[string]Value, error) {
	if len(v.Raw()) == 0 {
		return map[string]Value{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(v.Raw(), &raw); err != nil {
		return nil, fmt.Errorf("object patch against a non-object: %w", err)
	}
	out := make(map[string]Value, len(raw))
	for k, r := range raw {
		out[k] = NewValue(r)
	}
	return out, nil
}

func encodeObject(m map[string]Value) (Value, error) {
	raw := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		raw[k] = v.Raw()
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return Value{}, err
	}
	return NewValue(b), nil
}

// decodeList reads a value as a keyed list: an array of {Key, Value}.
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

// IsEmpty is IsIdentity under the name call sites already use.
// is there now. One traversal; Keys, removals and set values are views of it.
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

// expand yields a set value as the leaf paths it declares, so a nested object
// arrives addressable rather than as one blob.
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

// Entry is the patch's entry for one path, if it touches it.
func (p Patch) Entry(key string) (Entry, bool) {
	for _, e := range p.Entries() {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

// Build is the only way to make a patch from intent rather than from a diff:
// state what a board should hold and diff the result. A patch is a difference,
// so it cannot be assembled field by field.
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
	return next.Diff(base)
}
