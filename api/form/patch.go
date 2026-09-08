package form

import (
	"encoding/json"
	"fmt"
	"sort"
)

// A patch describes the change between two values, and describes ITSELF: the
// reader dispatches on which shape is populated rather than consulting a
// schema. Figaro's form is open -- arbitrary keys, raw JSON -- so there is no
// declared type to generate a patch shape from, and none is wanted.
//
// Exactly one shape is non-nil. The zero Patch is Identity.
//
// NAMED Patch ONLY WHILE THE FLAT ONE STILL EXISTS. It becomes Patch
// when the flat implementation is removed; form.Patch is an alias to
// message.Patch today and the two cannot share a name.
//
//	{Before, After}                  a scalar changed
//	{Set, Delete, Update}            an object gained, lost or altered keys
//	{Create, Delete, Update, Order}  a keyed list changed
//
// EVERY OPERATION CARRIES WHAT IT DESTROYED. Delete holds the value it
// removed, a scalar change holds Before as well as After, so Inverse is a
// mechanical swap at every level rather than something reconstructed by
// replaying history. That is what makes undo, revert and conflict resolution
// possible from the patch alone.
type Patch struct {
	Scalar *ScalarPatch `json:"scalar,omitempty"`
	Object *ObjectPatch `json:"object,omitempty"`
	List   *ListPatch   `json:"list,omitempty"`
}

// ScalarPatch is a whole-value swap: the leaf of every recursion.
type ScalarPatch struct {
	Before Value `json:"Before"`
	After  Value `json:"After"`
}

// ObjectPatch changes the keys of an object.
//
// SET IS CREATE-ONLY. A key that already exists is altered through Update,
// which recurses and bottoms out at a ScalarPatch carrying Before. Letting Set
// overwrite would put a change in the patch with no record of what it replaced,
// and Inverse would stop being total.
type ObjectPatch struct {
	Set    map[string]Value `json:"Set,omitempty"`
	Delete map[string]Value `json:"Delete,omitempty"`
	Update map[string]Patch `json:"Update,omitempty"`
}

// ListPatch changes a keyed, ordered collection. Items are addressed by KEY,
// never by index: two writers inserting "at position 3" are not touching the
// same thing, and reconciling position is Order's separate job.
type ListPatch struct {
	Create []KeyedValue     `json:"Create,omitempty"`
	Delete []KeyedValue     `json:"Delete,omitempty"`
	Update map[string]Patch `json:"Update,omitempty"`
	Order  *Order           `json:"Order,omitempty"`
}

// KeyedValue is a list item and the key that identifies it. The key is the
// item's identity for the life of the list and is supplied by whoever creates
// it; it must be unique within that list.
type KeyedValue struct {
	Key   string `json:"Key"`
	Value Value  `json:"Value"`
}

// Order is a PARTIAL WINDOW onto the new order: the keys that moved, plus one
// neighbour either side as an anchor. Everything undeclared keeps its original
// position, so an append costs two entries rather than the whole list.
//
// Moved indexes into Declared and names the keys that actually changed
// position -- what a reader renders as "this moved", and what a merge treats
// as a reorder rather than an incidental shift.
type Order struct {
	Declared []string `json:"Declared"`
	Moved    []int    `json:"Moved"`

	// Prior is the same window in the order it came FROM, so Inverse stays a
	// swap rather than a recomputation against a snapshot it does not hold.
	//
	// IT IS NOT SIMPLY Declared REVERSED. A window describes the sequence
	// AFTER creates and deletes have landed, and the inverse patch creates
	// what this one deleted -- so the two directions are windows over
	// different baselines and both are computed where both are known: at the
	// diff.
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
// the whole reason each operation carries what it destroyed.
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
	// KINDS DISAGREE, WHICH MEANS THE VALUE CHANGED SHAPE BETWEEN THE TWO
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

	// CREATES KEEP THEIR ORDER. Apply appends them in array order, so the
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
		// A DELETE FOLLOWED BY A CREATE IS NOT AN UPDATE, because a list has
		// position: the item leaves its seat and is appended. Collapsing the
		// pair keeps it where it was, which is a different list. Objects have
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

// keyedSeq is a keyed collection that remembers the order things were added.
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
func (p Patch) IsEmpty() bool { return p.IsIdentity() }

// Build is how a caller with a BASE makes a patch: state the keys it wants
// set or removed and diff the result.
//
// A patch is a difference, not a command. Producing one this way is what gives
// it the prior values that make it invertible -- a bare "set these keys" has
// no record of what it replaced and cannot be undone.
func Build(base Snapshot, set map[string]json.RawMessage, remove []string) Patch {
	next := base
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		next = next.SetPath(k, set[k])
	}
	for _, k := range remove {
		next = next.DeletePath(k)
	}
	return next.Diff(base)
}

// Creates is Build against an empty board: every key is new. For a caller
// that holds no base, which can only be describing creation.
func Creates(set map[string]json.RawMessage) Patch {
	return Build(Snapshot{}, set, nil)
}

// creationsOnly drops everything that destroys, keeping what a board does not
// already hold. Recursive, because a nested object may create and delete at
// once.
func (p Patch) creationsOnly() Patch {
	switch {
	case p.Object != nil:
		out := &ObjectPatch{Set: p.Object.Set}
		for k, c := range p.Object.Update {
			if kept := c.creationsOnly(); !kept.IsIdentity() {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[k] = kept
			}
		}
		return Patch{Object: out}
	case p.List != nil:
		out := &ListPatch{Create: p.List.Create}
		for k, c := range p.List.Update {
			if kept := c.creationsOnly(); !kept.IsIdentity() {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[k] = kept
			}
		}
		return Patch{List: out}
	}
	return p
}

// Keys is every leaf path the patch touches, for callers that police a patch
// by key: protection, rendering, and the study mirror.
func (p Patch) Keys() []string {
	seen := map[string]bool{}
	collectKeys(p, "", seen)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func collectKeys(p Patch, prefix string, out map[string]bool) {
	join := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	switch {
	case p.Object != nil:
		for k := range p.Object.Set {
			out[join(k)] = true
		}
		for k := range p.Object.Delete {
			out[join(k)] = true
		}
		for k, c := range p.Object.Update {
			collectKeys(c, join(k), out)
		}
	case p.List != nil, p.Scalar != nil:
		if prefix != "" {
			out[prefix] = true
		}
	}
}

// Removes is every leaf path the patch deletes.
func (p Patch) Removes() []string {
	seen := map[string]bool{}
	collectRemoves(p, "", seen)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func collectRemoves(p Patch, prefix string, out map[string]bool) {
	join := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	if p.Object == nil {
		return
	}
	for k := range p.Object.Delete {
		out[join(k)] = true
	}
	for k, c := range p.Object.Update {
		collectRemoves(c, join(k), out)
	}
}

func sortStrings(s []string) { sort.Strings(s) }

// Leaves projects a patch to the leaf paths it SETS and their new values.
//
// It is the inverse of Creates for a creation patch, and it exists for
// callers that compose intent rather than apply it -- outfits layering TOML
// tables, the study mirror listing what changed. It is a PROJECTION, not the
// patch: removals and prior values are not in it, so it must not be used to
// apply anything.
func (p Patch) Leaves() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	collectLeaves(p, "", out)
	return out
}

func collectLeaves(p Patch, prefix string, out map[string]json.RawMessage) {
	join := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	switch {
	case p.Scalar != nil:
		if prefix != "" {
			out[prefix] = p.Scalar.After.Raw()
		}
	case p.Object != nil:
		for k, v := range p.Object.Set {
			flattenValue(v, join(k), out)
		}
		for k, c := range p.Object.Update {
			collectLeaves(c, join(k), out)
		}
	case p.List != nil:
		if prefix != "" {
			// A list is one value at its path: its members are addressed by
			// key inside it, not by a dotted path outside it.
			if raw, err := json.Marshal(p.List.Create); err == nil {
				out[prefix] = raw
			}
		}
	}
}

// flattenValue walks a value the patch SETS, so a nested object arrives as
// the leaf paths it declares rather than as one opaque blob.
func flattenValue(v Value, prefix string, out map[string]json.RawMessage) {
	obj, isObj := asObject(v)
	if !isObj || len(obj) == 0 {
		out[prefix] = v.Raw()
		return
	}
	for k, child := range obj {
		flattenValue(child, prefix+"."+k, out)
	}
}

// legacyPatch is the FLAT patch this replaced: a map of dotted keys to set and
// a list to remove, with no record of prior values.
type legacyPatch struct {
	Set    map[string]json.RawMessage `json:"set"`
	Remove []string                   `json:"remove"`
}

// UnmarshalJSON reads a patch, converting one written in the flat shape.
//
// THE LOG IS THE STATE. Every patch the form channel already holds is flat, and
// a structural decode of one would silently produce Identity -- the history
// would still be there and would reduce to an empty board. So the flat shape is
// recognised and lifted.
//
// What cannot be recovered is invertibility: a flat patch never recorded what
// it overwrote, so a converted one carries creations and deletions and no
// Before. Old history applies exactly as it did; it just cannot be undone.
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
		if len(old.Remove) > 0 {
			// Build against an empty board drops removals -- nothing is there
			// to remove -- but the record says they happened, so they are
			// restated where a reader can act on them.
			obj := p.Object
			if obj == nil {
				obj = &ObjectPatch{}
			}
			if obj.Delete == nil {
				obj.Delete = map[string]Value{}
			}
			for _, k := range old.Remove {
				obj.Delete[k] = Value{}
			}
			*p = Patch{Object: obj}
		}
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
