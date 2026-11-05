package angelus

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/models"

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
	models.FormID: {
		Rows: "models",
		GroupColumns: []rpc.ViewColumn{
			{Key: "refreshed_at", Title: "listed", Format: rpc.FormatAge},
			{Key: "error", Shrink: 1, Alert: true},
		},
		Columns: []rpc.ViewColumn{
			{Key: "", Title: "model", Shrink: 1},
			{Key: "name", Shrink: 2},
			{Key: "context.default", Title: "context", Right: true},
			{Key: "max_output", Title: "max out", Right: true},
		},
		Skip: "hidden",
		Mark: &rpc.ViewMark{GroupSubject: models.ProviderKey, RowSubject: models.ModelKey},
		Actions: []rpc.ViewAction{
			{Key: "Enter", Label: "use this model", Method: rpc.MethodModelsPick, Target: rpc.TargetSubject,
				Args: map[string]rpc.ViewArg{"provider": rpc.ArgGroup, "model": rpc.ArgRow}},
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
