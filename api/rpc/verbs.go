package rpc

type Verb string

const (
	VerbRead   Verb = "read"
	VerbListen Verb = "listen"
	VerbSend   Verb = "send"
	VerbFork   Verb = "fork"
	VerbSet    Verb = "set"
	VerbCreate Verb = "create"
	VerbMint   Verb = "mint"
	VerbKill   Verb = "kill"
	VerbAdmin  Verb = "admin"
	VerbUse    Verb = "use"
	VerbJoin   Verb = "join"
	VerbDecide Verb = "decide"
	VerbAdopt  Verb = "adopt"
	VerbLease  Verb = "lease"
	VerbSay    Verb = "say"
	VerbDelete Verb = "delete"

	VerbCast    Verb = "cast"
	VerbSucceed Verb = "succeed"
)

func Verbs() []Verb {
	return []Verb{VerbRead, VerbListen, VerbSend, VerbFork, VerbSet, VerbCreate, VerbMint, VerbKill, VerbAdmin, VerbUse, VerbJoin, VerbDecide, VerbAdopt, VerbLease, VerbSay, VerbDelete, VerbCast, VerbSucceed}
}

const VerbNone Verb = ""
