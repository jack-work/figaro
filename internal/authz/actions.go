package authz

import (
	"maps"
	"slices"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authn"
)

type Action struct {
	Verb    rpc.Verb
	Also    []rpc.Verb
	Handler bool
	Rows    func(aria string) bool
	On      func(Request) ([]Resource, error)
}

var actions = map[string]Action{
	rpc.MethodHello: {},

	rpc.MethodAriaFrame:  {Verb: rpc.VerbListen},
	rpc.MethodTurnDone:   {Verb: rpc.VerbListen},
	rpc.MethodFormDelta:  {Verb: rpc.VerbListen},
	rpc.MethodFollow:     {Verb: rpc.VerbListen, Rows: listingForm},
	rpc.MethodUnfollow:   {Verb: rpc.VerbListen, Rows: listingForm},
	rpc.MethodAriaClosed: {Verb: rpc.VerbListen},
	rpc.MethodDoorLapsed: {Verb: rpc.VerbListen},

	rpc.MethodQua:         {Verb: rpc.VerbSend},
	rpc.MethodInterrupt:   {Verb: rpc.VerbSend},
	rpc.MethodQueueUpdate: {Verb: rpc.VerbSend},
	rpc.MethodQueueDelete: {Verb: rpc.VerbSend},

	rpc.MethodDecide: {Verb: rpc.VerbDecide, Also: []rpc.Verb{rpc.VerbSend}, On: decideOn},

	rpc.MethodWorkerPages:   {Verb: rpc.VerbLease, On: poolOf},
	rpc.MethodWorkerLease:   {Verb: rpc.VerbLease, On: poolOf},
	rpc.MethodWorkerRelease: {Verb: rpc.VerbLease, On: poolOf},
	rpc.MethodWorkerDeliver: {Verb: rpc.VerbLease, On: poolOf},
	rpc.MethodWorkerSay:     {Verb: rpc.VerbSay, On: leaseOf},

	rpc.MethodQueued:  {Verb: rpc.VerbRead},
	rpc.MethodRead:    {Verb: rpc.VerbRead},
	rpc.MethodContext: {Verb: rpc.VerbRead},
	rpc.MethodForm:    {Verb: rpc.VerbRead, Rows: listingForm},
	rpc.MethodIR:      {Verb: rpc.VerbRead},
	rpc.MethodLineage: {Verb: rpc.VerbRead},
	rpc.MethodScopes:  {Verb: rpc.VerbRead},
	rpc.MethodGrants:  {Verb: rpc.VerbRead},
	rpc.MethodList:    {Verb: rpc.VerbRead, Rows: always},
	rpc.MethodAttach:  {Verb: rpc.VerbRead, Also: []rpc.Verb{rpc.VerbSend}, Rows: listingForm},
	rpc.MethodOutfits: {Verb: rpc.VerbRead},

	rpc.MethodModelsWant:    {Verb: rpc.VerbRead},
	rpc.MethodNodeResolve:   {Verb: rpc.VerbRead, Rows: always},
	rpc.MethodTroupeMembers: {Verb: rpc.VerbRead},
	rpc.MethodImportStatus:  {Verb: rpc.VerbRead, Handler: true},
	rpc.MethodNodeLearn:     {Verb: rpc.VerbRead, Handler: true},

	rpc.MethodSet:       {Verb: rpc.VerbSet, Rows: topologyForm},
	rpc.MethodFormApply: {Verb: rpc.VerbSet},
	rpc.MethodStudy:     {Verb: rpc.VerbSet},
	rpc.MethodDrop:      {Verb: rpc.VerbSet},
	rpc.MethodCast:      {Verb: rpc.VerbSet},
	rpc.MethodPromote:   {Verb: rpc.VerbSet},
	rpc.MethodDemote:    {Verb: rpc.VerbSet},
	rpc.MethodSeat:      {Verb: rpc.VerbSet},
	rpc.MethodPlace:     {Verb: rpc.VerbSet},

	rpc.MethodParked: {Verb: rpc.VerbRead},
	rpc.MethodAccept: {Verb: rpc.VerbAdopt},
	rpc.MethodRefuse: {Verb: rpc.VerbAdopt},

	rpc.MethodCreate:     {Verb: rpc.VerbMint},
	rpc.MethodFormCreate: {Verb: rpc.VerbCreate},
	rpc.MethodFormBind:   {Verb: rpc.VerbCreate},
	rpc.MethodImport:     {Verb: rpc.VerbCreate},

	rpc.MethodImportBegin:  {Verb: rpc.VerbCreate},
	rpc.MethodImportChunk:  {Verb: rpc.VerbCreate},
	rpc.MethodImportCommit: {Verb: rpc.VerbCreate},
	rpc.MethodImportAbort:  {Verb: rpc.VerbCreate},

	rpc.MethodFork:          {Verb: rpc.VerbFork},
	rpc.MethodKill:          {Verb: rpc.VerbKill},
	rpc.MethodReapedRestore: {Verb: rpc.VerbSet},
	rpc.MethodGCPlan:        {Verb: rpc.VerbKill, Handler: true},
	rpc.MethodGCApply:       {Verb: rpc.VerbKill, Handler: true},
	rpc.MethodGCUndo:        {Verb: rpc.VerbSet, Handler: true},
	rpc.MethodModelsPick:    {Verb: rpc.VerbSet},
	rpc.MethodMove:          {Verb: rpc.VerbKill},

	rpc.MethodNormalize:         {Verb: rpc.VerbAdmin},
	rpc.MethodGC:                {Verb: rpc.VerbAdmin},
	rpc.MethodModelsRefresh:     {Verb: rpc.VerbAdmin},
	rpc.MethodBind:              {Verb: rpc.VerbAdmin},
	rpc.MethodResolve:           {Verb: rpc.VerbAdmin},
	rpc.MethodUnbind:            {Verb: rpc.VerbAdmin},
	rpc.MethodHop:               {Verb: rpc.VerbAdmin},
	rpc.MethodAngelusInfo:       {Verb: rpc.VerbAdmin},
	rpc.MethodStatus:            {Verb: rpc.VerbAdmin},
	rpc.MethodMemCollect:        {Verb: rpc.VerbAdmin},
	rpc.MethodPprofArm:          {Verb: rpc.VerbAdmin},
	rpc.MethodResidents:         {Verb: rpc.VerbAdmin},
	rpc.MethodAdmission:         {Verb: rpc.VerbAdmin},
	rpc.MethodQuarantineList:    {Verb: rpc.VerbAdmin},
	rpc.MethodQuarantineRelease: {Verb: rpc.VerbAdmin},
	rpc.MethodOutfitReload:      {Verb: rpc.VerbAdmin},
	rpc.MethodConfigure:         {Verb: rpc.VerbAdmin},
	rpc.MethodSaveBindings:      {Verb: rpc.VerbAdmin},
	rpc.MethodHaltAndStop:       {Verb: rpc.VerbAdmin},
	rpc.MethodProviderLedger:    {Verb: rpc.VerbAdmin},
	rpc.MethodTroupeEntries:     {Verb: rpc.VerbAdmin, Handler: true},
	rpc.MethodTroupeLeave:       {Verb: rpc.VerbAdmin, Handler: true},

	rpc.MethodTroupePolicy:    {Verb: rpc.VerbRead},
	rpc.MethodTroupePolicySet: {Verb: rpc.VerbAdmin, Handler: true},

	rpc.MethodTroupeAPIKeyList:   {Verb: rpc.VerbRead, Handler: true},
	rpc.MethodTroupeAPIKeyMint:   {Verb: rpc.VerbAdmin, Handler: true},
	rpc.MethodTroupeAPIKeyRevoke: {Verb: rpc.VerbAdmin, Handler: true},

	rpc.MethodTroupeSign:     {Verb: rpc.VerbJoin, Handler: true},
	rpc.MethodTroupeIdentity: {Verb: rpc.VerbJoin, Handler: true},
}

func ActionOf(method string) (Action, bool) {
	a, ok := actions[method]
	return a, ok
}

func VerbOf(method string) (rpc.Verb, bool) {
	a, ok := actions[method]
	return a.Verb, ok
}

func VerbsOf(method string) ([]rpc.Verb, bool) {
	a, ok := actions[method]
	if !ok {
		return nil, false
	}
	return append([]rpc.Verb{a.Verb}, a.Also...), true
}

func DeclaredMethods() []string {
	return slices.Sorted(maps.Keys(actions))
}

func always(string) bool { return true }

func listingForm(aria string) bool {
	return aria == rpc.AriasFormID || aria == rpc.GlobalAriasFormID || aria == rpc.TopologyFormID || aria == rpc.TroupeFormID || aria == rpc.RecentFormID
}

func topologyForm(aria string) bool { return aria == rpc.TopologyFormID }

func (a Action) filtersRows(aria string) bool { return a.Rows != nil && a.Rows(aria) }

func (a Action) on(r Request) ([]Resource, error) {
	if a.On != nil {
		return a.On(r)
	}
	if r.Aria != "" {
		return []Resource{Aria(r.Aria)}, nil
	}
	return []Resource{Daemon}, nil
}

func poolOf(r Request) ([]Resource, error) {
	pool, err := poolNamed(r.Params)
	if err != nil {
		return nil, err
	}
	return []Resource{Pool(pool)}, nil
}

func leaseOf(r Request) ([]Resource, error) {
	box, _ := authn.BoxOf(r.Subject.Principal)
	return []Resource{Lease(box)}, nil
}

func decideOn(r Request) ([]Resource, error) { return decideResources(r.Aria, r.Params) }
