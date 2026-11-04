package form_test

import "github.com/jack-work/figaro/api/form"

func reduceForBench(s form.Snapshot, p form.Patch) (form.Snapshot, form.Patch) {
	c := s.Apply(p).Diff(s)
	return s.Apply(c), c
}
