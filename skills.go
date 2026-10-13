// Package figaro embeds skills/figaro so a one-file install ships the skill
// that explains how to drive figaro; internal/outfit unpacks it to disk once
// per content hash because the model reads chapters by path.
package figaro

import "embed"

// Skills holds skills/figaro verbatim, paths included ("skills/figaro/SKILL.md").
//
//go:embed all:skills/figaro
var Skills embed.FS
