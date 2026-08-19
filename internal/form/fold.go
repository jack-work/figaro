package form

import "text/template"

// Fold and FoldRender replaced SIX of the seven sites that folded form
// patches. The seventh, copilot's inputFor loop, still folds inline.
//
// It is not an oversight and not a retrofit: copilot PROPAGATES a render
// error where the other three log it and continue, so converting it needs
// FoldRender to support abort-on-error -- a change to this abstraction rather
// than a missed call site. Filed as plans/fold-seventh-site.md.
//
// The stage 1 commit message claimed seven. It converted six.

// Fold applies patches in order.
func Fold(s Snapshot, patches []Patch) Snapshot {
	for _, p := range patches {
		s = s.Apply(p)
	}
	return s
}

// FoldRender renders each patch against the board as it stood BEFORE that
// patch, then applies it. Rendering after would describe a change against the
// state it produced.
//
// visit receives every rendered entry in order; onErr receives a render
// failure and the fold continues. Either may be nil.
func FoldRender(s Snapshot, patches []Patch, tmpls *template.Template, visit func(RenderedEntry), onErr func(error)) Snapshot {
	for _, p := range patches {
		if tmpls != nil {
			rendered, err := Render(p, s, tmpls)
			if err != nil {
				if onErr != nil {
					onErr(err)
				}
			} else if visit != nil {
				for _, r := range rendered {
					visit(r)
				}
			}
		}
		s = s.Apply(p)
	}
	return s
}
