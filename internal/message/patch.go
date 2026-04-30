package message

import "github.com/jack-work/figaro/internal/chalkboard"

// Patch is the IR type for a chalkboard delta — a Set of new or
// changed keys plus a list of keys to Remove.
//
// The canonical home for this type is migrating to the message
// package as part of the aria-log-unification work
// (see plans/aria-storage/log-unification.md). For now, Patch is a
// type alias for chalkboard.Patch so the two names refer to the same
// underlying type and methods defined on chalkboard.Patch (IsEmpty,
// Entries, etc.) are directly available here. After the unification
// completes, the canonical definition will live in this package and
// chalkboard.Patch will become the alias.
type Patch = chalkboard.Patch
