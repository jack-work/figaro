package rpc

// THE WHOLE SURFACE, ON ONE SCREEN.
//
// Grouped by KIND, because that is the question a reader has: is this
// something I ask for, something that arrives unasked, or something only an
// operator does? The names deliberately say WHAT they do and not WHERE they
// are served -- a read has one name whichever door it arrives at, and the
// daemon routes (see the READS block).
//
// The rule that keeps this list short: a new method must state which existing
// method it is NOT. The May 2026 tightening cut this surface to five methods
// on the aria socket and left a shape but no rule; three months later it was
// forty, with three duplicate reads and a hand-maintained copy of a request
// type. plans/api-coherence.md.

// PUSHES: server -> client, unasked. A subscriber gets these on the
// connection it already holds; there is no second transport.
const (
	// MethodAriaFrame pushes one aria.Page snapshot or delta. The UI cursor
	// is a turn id plus, for backward paging, a node ordinal; some request
	// field names still say LT.
	MethodAriaFrame = "figaro.aria"
	// MethodTurnDone reports that a turn ended, with the resulting idle state.
	MethodTurnDone = "turn.done"
	// MethodFormDelta pushes one committed form patch, in the shape a client
	// SENDS one, so a listener is an ordinary subscriber.
	MethodFormDelta = "form.delta"
)

// THE TURN: what a client asks OF one aria.
const (
	MethodQua       = "figaro.qua"
	MethodInterrupt = "figaro.interrupt"
	MethodSet       = "figaro.set"
	// Reading the queue stays on MethodQueued; the two mutators are the U and
	// D of the CRUD. C is MethodQua -- a queued message IS a submitted
	// prompt, so there is deliberately no second create path.
	MethodQueued      = "figaro.queued"
	MethodQueueUpdate = "figaro.queue.update"
	MethodQueueDelete = "figaro.queue.delete"
)

// STUDY AND CAST: an aria's subscriptions to UNBOUND forms (durable under
// system.studies). Cast is the casting-call: serialize in the aria's actor
// loop, ensure the study, cross-call the role's writer to point target-aria
// here.
const (
	MethodStudy = "figaro.study"
	MethodDrop  = "figaro.drop"
	MethodCast  = "figaro.cast"
)

// THE READS. ONE NAME PER VERB, served on BOTH doors: an aria's own socket,
// where the connection says which aria, and the angelus, where
// ReadRequest.FigaroID does. The door decides nothing about the answer --
// routeRead forwards to a live agent when there is one (it holds the open
// streaming region, partial tool arguments and the in-flight turn) and reads
// the store when there is not, so a dormant aria is readable without being
// woken and a client never has to know which case it is in.
//
// aria.page, aria.context and aria.form were the second copy of these three,
// and AriaPageRequest the second copy of ReadRequest. Both died 2026-08-21.
const (
	// MethodRead pulls one aria.Page from a turn cursor: the catch-up half of
	// the shape MethodAriaFrame pushes, so a reconnecting client can rebuild
	// and then follow live frames on the same connection.
	MethodRead    = "figaro.read"
	MethodContext = "figaro.context"
	MethodForm    = "figaro.form"
	// MethodIR returns raw fig IR entries, through the angelus's shared
	// LogCache so live writes and reads do not race across processes. It is
	// the one read with no per-aria twin: MethodContext is the composed view,
	// this is the log itself.
	MethodIR = "figaro.ir"
)

// MethodNeedsAgent reports whether a method requires a running turn loop.
// It is the routing predicate, stated once: everything not named here needs
// an agent, so a new read must opt IN to being servable from the store.
func MethodNeedsAgent(method string) bool {
	switch method {
	case MethodRead, MethodContext, MethodForm:
		return false
	}
	return true
}

// TOPOLOGY: birth, fork, and removal. figaro.create and form.create are the
// two halves of the one birth verb.
const (
	MethodCreate = "figaro.create"
	// MethodFormCreate mints an UNBOUND FORM: fork the null root (or a form)
	// with a birth patch -- kind "form", @-sigiled id, no agent, no endpoint
	// activation beyond the hub.
	MethodFormCreate = "form.create"
	// MethodFormBind births a FIGARO from an unbound form: fork the form (or
	// the null root) with the caller's dressing plus runtime fill-ins, stamp
	// aria_id, stand the endpoint up, and construct NO agent -- the figaro is
	// born dormant and wakes on first need, which is where a missing provider
	// fails (`bind null` mints fine and errors at the first turn, by design).
	MethodFormBind  = "form.bind"
	MethodFork      = "figaro.fork"
	MethodPromote   = "figaro.promote"
	MethodImport    = "figaro.import"
	MethodNormalize = "figaro.normalize"
	MethodGC        = "figaro.gc"
	MethodKill      = "figaro.kill"
)

// SESSIONS: finding an aria, and the pid bindings a shell holds.
const (
	MethodList    = "figaro.list"
	MethodAttach  = "figaro.attach"
	MethodBind    = "pid.bind"
	MethodResolve = "pid.resolve"
	MethodUnbind  = "pid.unbind"
)

// ADMINISTRATION: the daemon's own state, which a client cannot read for
// itself -- the daemon may not even share a filesystem with the caller.
const (
	MethodAngelusInfo = "angelus.info"
	MethodStatus      = "angelus.status"
	// MethodOutfits answers what outfits exist and how one composes.
	MethodOutfits = "angelus.outfits"
	// MethodOutfitReload flags the default form for recomputation on the next
	// fig new. There is deliberately no inverse: outfit files are one-way
	// sources of truth.
	MethodOutfitReload = "outfit.reload"
	// MethodConfigure patches the server's configuration. The first-run
	// wizard is a client, so it cannot write config.toml itself.
	MethodConfigure    = "angelus.configure"
	MethodSaveBindings = "angelus.save_bindings"
	// MethodProviderLedger reads the daemon's recent provider round-trips. It
	// is a separate call rather than a field on angelus.status because the
	// answer is a list of hundreds of rows and status is polled.
	MethodProviderLedger = "angelus.provider_ledger"
)
