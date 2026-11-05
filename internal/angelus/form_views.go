package angelus

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/rpc"
)

var purgeParams, _ = json.Marshal(rpc.KillRequest{Recursive: true, Now: true})

var formViews = map[string]rpc.FormView{
	rpc.ReapedFormID: {
		List: "items",
		Columns: []rpc.ViewColumn{
			{Key: "", Title: "root"},
			{Key: "mantra", Shrink: 1},
			{Key: "nodes", Title: "arias", Format: rpc.FormatCount, Right: true},
			{Key: "at", Title: "reaped", Format: rpc.FormatTime},
			{Key: "purge_at", Title: "purged", Format: rpc.FormatTime},
		},
		Sort: "purge_at",
		Actions: []rpc.ViewAction{
			{Key: "r", Label: "restore", Method: rpc.MethodReapedRestore},
			{Key: "X", Label: "purge now", Method: rpc.MethodKill, Params: purgeParams, Confirm: true},
		},
	},
}

func formViewOf(id string) *rpc.FormView {
	v, ok := formViews[id]
	if !ok {
		return nil
	}
	return &v
}
