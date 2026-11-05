package angelus

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/models"

	"github.com/jack-work/figaro/api/rpc"
)

var purgeParams, _ = json.Marshal(rpc.KillRequest{Recursive: true, Now: true})

var formViews = map[string]rpc.FormView{
	rpc.ReapedFormID: {
		Rows: []string{"items.*"},
		Open: []string{"items"},
		Sort: "purge_at",
		Actions: []rpc.ViewAction{
			{Key: "r", Label: "restore", Method: rpc.MethodReapedRestore},
			{Key: "X", Label: "purge now", Method: rpc.MethodKill, Params: purgeParams, Confirm: true},
		},
	},
	models.FormID: {
		Rows: []string{"*.models.*"},
		Open: []string{"*", "*.models"},
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
