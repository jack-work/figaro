# This was cut from /home/gluck/dev/figaro-qua/main/plans/form-deltas-in-ui-ir.md for further review.

______________________________________________________________________

## 7. The fork-on-a-form bug, confirmed (same session, filed here for the same hand)

`fig fork @form` births a **figaro**, not a forked form: verified live, the
child is `kind=conversation` with `aria_id` and `system.forked_from` on its
board. So `fork` and `bind` on a form are SYNONYMS and one of the two names
is a lie.

Cause, one line: `XwalStore.ForkWith` hard-codes
`forkWithKind(..., kindConversation)`, while `forkWithKind`'s own comment
says the caller chooses the child's species. The fork verb has no way to say.

**The form→form operation already exists and is already documented**:
`CreateForm(parent, patch)`, *"Parent "" forks the null root; a form id
duplicates that form's state into a fresh @id"* (`rpc.FormCreateRequest`).
`createFormAndReport` even takes a `parent`. **Every CLI caller passes ""**,
so nothing reaches it. `fig form new` while attended to a form mints a
SIBLING off the null root and inherits nothing, verified.

So the fix is wiring, not mechanism: `fig fork` on a node of kind `form`
routes to `CreateForm(parent)`. Two real questions with it:

1. a form's timeline is one ceremonial record, so `<id>:<turn>` addresses
   nothing, a form fork is always at the head, and the verb should say so
   rather than silently ignoring a coordinate.
1. `CreateForm` REQUIRES a patch (*"a fork that transforms nothing is a fork
   nobody can name"*) and `fig fork` takes none. Either fork-a-form demands
   `-S`, or that rule is relaxed for this path. That is a ruling, not an
   implementation detail.

Recorded and NOT to be built (Gluck, explicitly): `cast` on a form with no
argument minting a figaro, and a role fork meaning fork-both-and-recast.
