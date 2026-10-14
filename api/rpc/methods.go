package rpc

// Methods are named by what they do, not where they are served: a read has
// one name whichever door it arrives at. A new method must state which
// existing method it is not.

// Pushes: server -> client, on the connection the subscriber already holds.
const (
	// MethodAriaFrame pushes one aria.Page snapshot or delta.
	MethodAriaFrame = "figaro.aria"
	MethodTurnDone  = "turn.done"
	// MethodFormDelta pushes one committed form patch in the shape a client
	// sends one.
	MethodFormDelta = "form.delta"
)

// The turn: what a client asks of one aria.
const (
	MethodQua       = "figaro.qua"
	MethodInterrupt = "figaro.interrupt"
	MethodSet       = "figaro.set"
	// A queued message is a submitted prompt: MethodQua is the create path.
	MethodQueued      = "figaro.queued"
	MethodQueueUpdate = "figaro.queue.update"
	MethodQueueDelete = "figaro.queue.delete"
)

// Study and cast: an aria's subscriptions to unbound forms, durable under
// system.studies.
const (
	MethodStudy = "figaro.study"
	MethodDrop  = "figaro.drop"
	MethodCast  = "figaro.cast"
)

// Reads are served on both doors: the aria socket, where the connection says
// which aria, and the angelus, where ReadRequest.FigaroID does. A live agent
// answers when there is one; otherwise the store does, without waking it.
const (
	// MethodRead is the catch-up half of MethodAriaFrame.
	MethodRead    = "figaro.read"
	MethodContext = "figaro.context"
	MethodForm    = "figaro.form"
	// MethodIR returns raw fig IR entries through the angelus's shared
	// LogCache; it has no per-aria twin.
	MethodIR = "figaro.ir"
	// MethodLineage returns the tree shape and the turn at which two paths
	// differ, no conversation.
	MethodLineage = "figaro.lineage"
)

// MethodNeedsAgent is the routing predicate: a new read must opt in to being
// servable from the store.
func MethodNeedsAgent(method string) bool {
	switch method {
	case MethodRead, MethodContext, MethodForm:
		return false
	}
	return true
}

const MethodDecide = "figaro.decide"

// Topology: birth, fork, and removal.
const (
	MethodCreate = "figaro.create"
	// MethodFormCreate mints an unbound form: @-sigiled id, no agent.
	MethodFormCreate = "form.create"
	// MethodFormBind births a figaro from an unbound form, dormant: a missing
	// provider fails at the first turn, not at bind.
	MethodFormBind  = "form.bind"
	MethodFork      = "figaro.fork"
	MethodPromote   = "figaro.promote"
	MethodImport    = "figaro.import"
	MethodNormalize = "figaro.normalize"
	MethodGC        = "figaro.gc"
	MethodKill      = "figaro.kill"
)

// Sessions: finding an aria, and the pid bindings a shell holds.
const (
	MethodList    = "figaro.list"
	MethodAttach  = "figaro.attach"
	MethodBind    = "pid.bind"
	MethodResolve = "pid.resolve"
	MethodUnbind  = "pid.unbind"
)

// Administration: the daemon's own state, which may not share a filesystem
// with the caller.
const (
	// MethodHello is the node door (api/rpc/door.go); first call on a
	// connection.
	MethodHello       = "angelus.hello"
	MethodAngelusInfo = "angelus.info"
	MethodStatus      = "angelus.status"
	// MethodMemCollect stops the world; never call it from a ticker.
	MethodMemCollect = "angelus.mem.collect"
	MethodOutfits    = "angelus.outfits"
	// MethodOutfitReload flags the default form for recomputation on the next
	// create; outfit files are one-way sources of truth.
	MethodOutfitReload   = "outfit.reload"
	MethodConfigure      = "angelus.configure"
	MethodSaveBindings   = "angelus.save_bindings"
	MethodProviderLedger = "angelus.provider_ledger"
	MethodModelsWant     = "angelus.models.want"
)
