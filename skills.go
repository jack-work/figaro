// Package figaro exists for one reason: to carry figaro's first-party skills
// INSIDE the binary.
//
// A skill is only useful to a model if it has a path: the form hands the model
// a `filePath` and the model reads deeper chapters from beside it. So the
// embedded copy is not served from memory; internal/outfit unpacks it onto
// disk once per content hash and points the loader at that directory. See
// internal/outfit/bundled.go for the unpacking and the precedence rules.
//
// Only `skills/figaro` is embedded, and that is deliberate. It is the skill
// that explains how to drive figaro at all, which is exactly what an agent
// cannot look up if it does not already have it; everything else in `skills/`
// is a skill a user chooses and copies into their config dir. A one line
// install must therefore ship exactly one file, and this is how the skill
// rides along in it.
//
// The embed directive is also the build-time guarantee that replaced a
// postInstall check in flake.nix: delete the directory and compilation fails,
// rather than a binary shipping with skills that silently do not exist.
package figaro

import "embed"

// Skills holds `skills/figaro` verbatim, paths included: entries are named
// "skills/figaro/SKILL.md" and so on. The `all:` prefix keeps files whose
// names begin with "." or "_" (none today, but a skill is a documentation
// tree and the next chapter should not vanish because of its filename).
//
//go:embed all:skills/figaro
var Skills embed.FS
