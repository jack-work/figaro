package angelus

import "github.com/jack-work/figaro/api/rpc"

var formViews = map[string]rpc.FormView{}

func formViewOf(id string) *rpc.FormView {
	v, ok := formViews[id]
	if !ok {
		return nil
	}
	return &v
}
