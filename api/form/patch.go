package form

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

	// New names the Update keys that did not exist. Their leaves are
	// described rather than the subtree, so two patches adding different
	// fields under one parent compose; the flag is what lets Inverse remove
	// the parent instead of leaving it empty.
	New map[string]bool `json:"New,omitempty"`
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
				if p.Object.New[k] {
					// The key was created here, so undoing it removes the
					// key rather than emptying it. What it held is what the
					// forward patch builds from nothing.
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
	src, isObj := v.members()
	if !isObj && len(v.Raw()) > 0 && string(v.Raw()) != "null" {
		return Value{}, fmt.Errorf("object patch against a non-object")
	}
	// Copied because the memo behind members() is shared with every other
	// reader of this value.
	obj := make(map[string]Value, len(src)+len(p.Object.Set))
	for k, val := range src {
		obj[k] = val
	}
	for k, nv := range p.Object.Set {
		// A write of an equal value keeps the bytes already stored. Providers
		// cache on exact bytes, so a rewrite that changes only key order or
		// spacing must not perturb the board.
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
			// Updating a key the board lacks creates it: that is how a patch
			// describing new leaves under a new parent applies.
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

// encodeObject writes the members verbatim: their bytes are already JSON and
// re-marshalling them was the cost of every apply.
func encodeObject(m map[string]Value) Value {
	raw := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		raw[k] = v.Raw()
	}
	return NewValue(encodeRawObject(raw))
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

// Entry is what the patch does at one path.
//
// It walks the patch rather than scanning Entries, because a value the patch
// SETS may itself be an object: Entries expands it so a nested field is
// visible to protection, while a lookup of the key itself must still answer
// with the whole value that was written.
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
			// The patch set a whole object and the path continues inside it.
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
				// The key is created here and its leaves are described, so
				// the value it ends up holding is what the child builds from
				// nothing.
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

// longestPatchMember picks the longest member name of the patch level that is
// a dotted prefix of rest.
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
	p := next.Diff(base)

	// A removal the base cannot express is still a removal. Diffing alone
	// drops it: nothing was there to delete, so nothing changed. The patch
	// must still say so, carrying the prior value where the base knew one.
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

// legacyPatch is the flat wire shape: dotted keys to set, a list to remove.
type legacyPatch struct {
	Set    map[string]json.RawMessage `json:"set"`
	Remove []string                   `json:"remove"`
}

// UnmarshalJSON reads a patch, lifting one written in the flat shape.
//
// Every patch already on the form channel is flat. Decoding one structurally
// yields Identity, so the history would still be on disk and would reduce to
// an empty board.
//
// A converted patch carries no prior values: the flat shape never recorded
// them. It applies exactly as it did; it cannot be inverted.
func (p *Patch) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	_, hasSet := probe["set"]
	_, hasRemove := probe["remove"]
	if hasSet || hasRemove {
		var old legacyPatch
		if err := json.Unmarshal(data, &old); err != nil {
			return err
		}
		*p = Build(Snapshot{}, old.Set, old.Remove)
		return nil
	}
	type plain Patch // no recursion through this method
	var out plain
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*p = Patch(out)
	return nil
}

// valueAt walks a value by dotted path, longest member first.
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

// deleteAt is a patch that removes the leaf at segs, nesting the operation so
// it reaches the node the path names rather than a member of the root.
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
