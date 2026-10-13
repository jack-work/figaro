package form

import "text/template"

func Fold(s Snapshot, patches []Patch) Snapshot {
	for _, p := range patches {
		s = s.Apply(p)
	}
	return s
}

// FoldRender renders each patch against the board as it stood before that
// patch.
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
