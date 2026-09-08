package message

import "github.com/jack-work/figaro/api/form"

// Patch is a form delta. It is form.Patch: a STRUCTURAL patch that reaches
// any node of the board and carries what it destroyed, so it can be inverted.
//
// top-level keys with no record of prior values, which could neither address
// a nested field nor be undone.
type Patch = form.Patch
