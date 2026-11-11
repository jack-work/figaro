# Layer 6: bindings off the host

## For Gluck (phone page)

Not drafted yet. Brought after the layers above it are settled.

**Scope.** The figaro shell (internal/figaro: form, server, study, projector, read) imports authz for principals, livelog/aria for the transcript stream, and outfit. 8 edges.

## Ledger

- Status: listed 2026-11-10, not started.
- Rulings: none yet.
- Open questions: none yet.
- Hidden variables surfaced: none yet.
- Crossing work: chat-aria steps 5-6 (study a log, wake mode) are bindings. Advised 2026-11-11: no new internal/figaro -> authz edge; sided checks run in the host, bindings take an authorized reader or an injected check. authz.Resource.Side stays an opaque string.
- Rollbacks: none.
