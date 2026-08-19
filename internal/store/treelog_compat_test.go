package store

import fwtree "github.com/jack-work/figaro/internal/store/tree"

// THE OLD CONSTRUCTORS, KEPT ONLY FOR THE TESTS THAT STILL ASSERT A LIVE
// PROPERTY. cachedLog is gone; these build the tree-backed log over the same
// substrate with a budget of its own.
//
// The row-count window and the inflation ratio are DROPPED rather than
// translated: the tree bounds bytes and there is no second unit to reconcile.
// A test that asserted a row-count window is asserting a policy that no longer
// exists, and it belongs in the deletion rather than in a shim.
func newWindowedLog[T any](inner Log[T], window, budget, num, denom int, sizeOf func(Entry[T]) int) *treeLog[T] {
	cache := NewIRCache[T](fwtree.NewBudget(int64(budget)), func(string) Log[T] { return inner }, sizeOf, irKey[T])
	return newTreeLog[T](inner, "aria", cache, sizeOf, irKey[T], nil)
}

// newSeededLog's SEED IS IGNORED: a fork's prefix is its ancestor's runs now,
// so there is nothing to donate. The signature survives only so the seam tests
// can be read against the new behaviour before they are rewritten or deleted.
func newSeededLog[T any](inner Log[T], window, budget, num, denom int, sizeOf func(Entry[T]) int, seed []Entry[T]) *treeLog[T] {
	return newWindowedLog[T](inner, window, budget, num, denom, sizeOf)
}
