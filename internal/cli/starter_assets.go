// Package cli — starter assets shipped in the binary.
//
// These are embedded so a fresh `go install` of figaro can scaffold a
// usable config without external files. First-run writes them to the
// user's ~/.config/figaro tree; the user is then free to edit, delete,
// or extend them.
package cli

import _ "embed"

// starterHowToSkill is the onboarding skill written into a new user's
// ~/.config/figaro/skills/howto.md by the first-run wizard. It teaches
// the agent how to teach a new user — calibrate skill level, walk the
// concepts in order, verify each step.
//
//go:embed starter_howto.md
var starterHowToSkill string
