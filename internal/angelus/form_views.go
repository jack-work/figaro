package angelus

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/models"

	"github.com/jack-work/figaro/api/rpc"
)

var purgeParams, _ = json.Marshal(rpc.KillRequest{Recursive: true, Now: true})

var ariaRowActions = []rpc.ViewAction{
	{Key: "Enter", Label: "attend", Local: rpc.ViewAttend},
	{Key: "x", Label: "kill", Method: rpc.MethodKill, Confirm: true},
}

var formViews = map[string]rpc.FormView{
	rpc.ReapedFormID: {
		Rows:  []string{"items.*"},
		Open:  []string{"items"},
		Sort:  "purge_at",
		Label: "mantra",
		Actions: []rpc.ViewAction{
			{Key: "r", Label: "restore", Method: rpc.MethodReapedRestore},
			{Key: "X", Label: "purge now", Method: rpc.MethodKill, Params: purgeParams, Confirm: true},
		},
	},
	rpc.TroupeFormID: {
		Rows:  []string{"*"},
		Mount: "form",
		Mark:  &rpc.ViewMark{Key: "self", Equals: "true"},
	},
	rpc.AriasFormID: {
		Rows:    []string{"arias.*", "**.forks.*"},
		Open:    []string{"arias", "**.forks"},
		RowAria: "id",
		Through: "forks",
		Label:   "mantra",
		Sort:    "-active_at",
		Mark:    &rpc.ViewMark{Key: "id", KeySubject: "aria_id"},
		Overlay: &rpc.ViewOverlay{Form: rpc.TopologyFormID, Key: "parent"},
		Actions: ariaRowActions,
	},
	rpc.RecentFormID: {
		Rows:    []string{"*"},
		RowAria: "id",
		Label:   "title",
		Aside:   "active_at",
		Sort:    "-active_at",
		Mark:    &rpc.ViewMark{Key: "id", KeySubject: "aria_id"},
		Actions: ariaRowActions,
	},
	models.FormID: {
		Rows:  []string{"*.models.*"},
		Open:  []string{"*", "*.models"},
		Skip:  "hidden",
		Label: "name",
		Mark:  &rpc.ViewMark{GroupSubject: models.ProviderKey, RowSubject: models.ModelKey},
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
