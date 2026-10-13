package store

import fwtree "github.com/jack-work/figaro/internal/store/tree"

// The old constructors, kept for the tests that still assert a live
// property: they build the tree-backed log over the same substrate with a
// budget of its own. The row-count window is dropped, not translated.
func newWindowedLog[T any](inner Log[T], window, budget, num, denom int, sizeOf func(Entry[T]) int) *treeLog[T] {
	cache := NewIRCache[T](fwtree.NewBudget(int64(budget)), func(string) Log[T] { return inner }, sizeOf, irKey[T])
	return newTreeLog[T](inner, "aria", cache, sizeOf, irKey[T], nil)
}

// newSeededLog's seed is ignored: a fork's prefix is its ancestor's runs now.
func newSeededLog[T any](inner Log[T], window, budget, num, denom int, sizeOf func(Entry[T]) int, seed []Entry[T]) *treeLog[T] {
	return newWindowedLog[T](inner, window, budget, num, denom, sizeOf)
}
