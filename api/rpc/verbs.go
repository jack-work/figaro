package rpc

type Verb string

const (
	VerbRead   Verb = "read"
	VerbListen Verb = "listen"
	VerbSend   Verb = "send"
	VerbFork   Verb = "fork"
	VerbSet    Verb = "set"
	VerbCreate Verb = "create"
	VerbKill   Verb = "kill"
	VerbAdmin  Verb = "admin"
	VerbUse    Verb = "use"
	VerbJoin   Verb = "join"
	VerbAdopt  Verb = "adopt"
)

func Verbs() []Verb {
	return []Verb{VerbRead, VerbListen, VerbSend, VerbFork, VerbSet, VerbCreate, VerbKill, VerbAdmin, VerbUse, VerbJoin, VerbAdopt}
}

const VerbNone Verb = ""

var methodVerbs = map[string]Verb{
	MethodHello: VerbNone,

	MethodAriaFrame:  VerbListen,
	MethodTurnDone:   VerbListen,
	MethodFormDelta:  VerbListen,
	MethodFollow:     VerbListen,
	MethodUnfollow:   VerbListen,
	MethodAriaClosed: VerbListen,
	MethodDoorLapsed: VerbListen,

	MethodQua:         VerbSend,
	MethodInterrupt:   VerbSend,
	MethodQueueUpdate: VerbSend,
	MethodQueueDelete: VerbSend,
	MethodDecide:      VerbSend,

	MethodQueued:  VerbRead,
	MethodRead:    VerbRead,
	MethodContext: VerbRead,
	MethodForm:    VerbRead,
	MethodIR:      VerbRead,
	MethodLineage: VerbRead,
	MethodScopes:  VerbRead,
	MethodGrants:  VerbRead,
	MethodList:    VerbRead,
	MethodAttach:  VerbRead,
	MethodOutfits: VerbRead,

	MethodModelsWant:    VerbRead,
	MethodNodeResolve:   VerbRead,
	MethodTroupeMembers: VerbRead,
	MethodImportStatus:  VerbRead,
	MethodNodeLearn:     VerbRead,

	MethodSet:     VerbSet,
	MethodStudy:   VerbSet,
	MethodDrop:    VerbSet,
	MethodCast:    VerbSet,
	MethodPromote: VerbSet,
	MethodDemote:  VerbSet,

	MethodParked: VerbRead,
	MethodAccept: VerbAdopt,
	MethodRefuse: VerbAdopt,

	MethodCreate:     VerbCreate,
	MethodFormCreate: VerbCreate,
	MethodFormBind:   VerbCreate,
	MethodImport:     VerbCreate,

	MethodImportBegin:  VerbCreate,
	MethodImportChunk:  VerbCreate,
	MethodImportCommit: VerbCreate,
	MethodImportAbort:  VerbCreate,

	MethodFork:          VerbFork,
	MethodKill:          VerbKill,
	MethodReapedRestore: VerbSet,
	MethodGCPlan:        VerbKill,
	MethodGCApply:       VerbKill,
	MethodGCUndo:        VerbSet,
	MethodModelsPick:    VerbSet,
	MethodMove:          VerbKill,

	MethodNormalize:         VerbAdmin,
	MethodGC:                VerbAdmin,
	MethodModelsRefresh:     VerbAdmin,
	MethodBind:              VerbAdmin,
	MethodResolve:           VerbAdmin,
	MethodUnbind:            VerbAdmin,
	MethodHop:               VerbAdmin,
	MethodAngelusInfo:       VerbAdmin,
	MethodStatus:            VerbAdmin,
	MethodMemCollect:        VerbAdmin,
	MethodPprofArm:          VerbAdmin,
	MethodQuarantineList:    VerbAdmin,
	MethodQuarantineRelease: VerbAdmin,
	MethodOutfitReload:      VerbAdmin,
	MethodConfigure:         VerbAdmin,
	MethodSaveBindings:      VerbAdmin,
	MethodHaltAndStop:       VerbAdmin,
	MethodProviderLedger:    VerbAdmin,
	MethodTroupeEntries:     VerbAdmin,
	MethodTroupeLeave:       VerbAdmin,

	MethodTroupePolicy:    VerbRead,
	MethodTroupePolicySet: VerbAdmin,

	MethodTroupeAPIKeyList:   VerbRead,
	MethodTroupeAPIKeyMint:   VerbAdmin,
	MethodTroupeAPIKeyRevoke: VerbAdmin,

	MethodTroupeSign:     VerbJoin,
	MethodTroupeIdentity: VerbJoin,
}

func VerbOf(method string) (Verb, bool) {
	v, ok := methodVerbs[method]
	return v, ok
}

var sufficientVerbs = map[string][]Verb{
	MethodAttach: {VerbSend},
}

func VerbsOf(method string) ([]Verb, bool) {
	v, ok := methodVerbs[method]
	if !ok {
		return nil, false
	}
	return append([]Verb{v}, sufficientVerbs[method]...), true
}
