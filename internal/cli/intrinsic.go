package cli

import "strings"

// THE ADDRESS GRAMMAR: `<host>/<intrinsic>`.
//
// A INTRINSIC FORM is a non-persistent builtin form bound to a host form, published
// by the harness and addressed as a named segment of its host. `state` is the
// reserved IDENTITY segment: it names the host's OWN form and is implied, so
//
//	fig form show <id>          and
//	fig form show <id>/state
//
// are the same address in every respect. What "the host's own form" means per
// host kind:
//
//   - a bound figaro  -> its bound form (its board)
//   - an unbound form -> that form's own state
//   - a role          -> the role form's own state, NOT its target's
//
// The third was already the behaviour -- openFormView resolves through
// resolveTargetEndpoint, which does not follow target-aria -- and naming it
// turns a rule you had to know into one the grammar states.
//
// `/` was unclaimed: `:` is a turn coordinate, `.` an LT, `@` a form sigil.
// So the grammar loses a special case rather than gaining one: `<id>` is no
// longer a DIFFERENT KIND of address from `<id>/queue`, it is an abbreviation
// of a sibling, and the host is simply the segment that is not a intrinsic.
const (
	intrinsicState   = "state"
	intrinsicRuntime = "runtime"
	intrinsicQueue   = "queue"
)

// splitIntrinsic pulls the intrinsic off an address. The returned name is "" for
// the identity segment, so a caller that does not care about intrinsic forms can
// ignore it entirely and still be correct.
func splitIntrinsic(spec string) (host, intrinsic string) {
	i := strings.LastIndexByte(spec, '/')
	if i < 0 {
		return spec, ""
	}
	host, name := spec[:i], spec[i+1:]
	if name == intrinsicState {
		return host, "" // the identity segment IS the host's own form
	}
	return host, name
}

// knownIntrinsic reports whether a name is one this build publishes. An unknown
// segment is not silently treated as the board: a reader who asks for
// `<id>/qeueu` must be told, or they will watch a board and believe it is a
// queue.
func knownIntrinsic(name string) bool {
	switch name {
	case "", intrinsicState, intrinsicRuntime, intrinsicQueue:
		return true
	}
	return false
}

// intrinsicNames is what a listing or a completion offers.
func intrinsicNames() []string { return []string{intrinsicRuntime, intrinsicQueue} }

// formAddress spells `<host>/<intrinsic>`, eliding the identity segment because
// it is implied. Round-trips with splitIntrinsic.
func formAddress(host, intrinsic string) string {
	if intrinsic == "" || intrinsic == intrinsicState {
		return host
	}
	return host + "/" + intrinsic
}
