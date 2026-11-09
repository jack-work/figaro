package store

import "github.com/jack-work/figaro/figwal/segment"

func OpenSegments() (count, headerBytes int64) { return segment.OpenSegments() }

type FormResidency struct {
	Forms           int
	SubscribedForms int
	LibrettoForms   int
	WindowPatches   int
	StateBytes      int64
	PatchBytes      int64
	LibrettoBytes   int64
}

func (f *Form) residentBytes() (state, patches int64, n int) {
	st := f.state.Load()
	state = int64(st.snap.ApproxBytes())
	for _, vp := range st.patches {
		patches += int64(vp.Patch.ApproxBytes())
	}
	return state, patches, len(st.patches)
}

func (b *XwalBackend) FormResidency() FormResidency {
	b.mu.Lock()
	forms := make([]*Form, 0, len(b.forms))
	for _, f := range b.forms {
		forms = append(forms, f)
	}
	libs := make([]*Form, 0, len(b.librettos))
	for _, l := range b.librettos {
		libs = append(libs, l.form)
	}
	b.mu.Unlock()

	var r FormResidency
	for _, f := range forms {
		state, patches, n := f.residentBytes()
		r.Forms++
		if f.Subscribed() {
			r.SubscribedForms++
		}
		r.WindowPatches += n
		r.StateBytes += state
		r.PatchBytes += patches
	}
	for _, f := range libs {
		state, patches, n := f.residentBytes()
		r.LibrettoForms++
		r.WindowPatches += n
		r.StateBytes += state
		r.PatchBytes += patches
		r.LibrettoBytes += state + patches
	}
	return r
}
