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
)

func Verbs() []Verb {
	return []Verb{VerbRead, VerbListen, VerbSend, VerbFork, VerbSet, VerbCreate, VerbKill, VerbAdmin, VerbUse}
}

const VerbNone Verb = ""

var methodVerbs = map[string]Verb{
	MethodHello: VerbNone,

	MethodAriaFrame:  VerbListen,
	MethodTurnDone:   VerbListen,
	MethodFormDelta:  VerbListen,
	MethodFollow:     VerbListen,
	MethodAriaClosed: VerbListen,

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
	MethodList:    VerbRead,
	MethodAttach:  VerbRead,
	MethodOutfits: VerbRead,

	MethodModelsWant:  VerbRead,
	MethodNodeResolve: VerbRead,

	MethodSet:     VerbSet,
	MethodStudy:   VerbSet,
	MethodDrop:    VerbSet,
	MethodCast:    VerbSet,
	MethodPromote: VerbSet,

	MethodCreate:     VerbCreate,
	MethodFormCreate: VerbCreate,
	MethodFormBind:   VerbCreate,
	MethodImport:     VerbCreate,

	MethodFork: VerbFork,
	MethodKill: VerbKill,

	MethodNormalize:      VerbAdmin,
	MethodGC:             VerbAdmin,
	MethodBind:           VerbAdmin,
	MethodResolve:        VerbAdmin,
	MethodUnbind:         VerbAdmin,
	MethodAngelusInfo:    VerbAdmin,
	MethodStatus:         VerbAdmin,
	MethodMemCollect:     VerbAdmin,
	MethodOutfitReload:   VerbAdmin,
	MethodConfigure:      VerbAdmin,
	MethodSaveBindings:   VerbAdmin,
	MethodProviderLedger: VerbAdmin,
}

func VerbOf(method string) (Verb, bool) {
	v, ok := methodVerbs[method]
	return v, ok
}
