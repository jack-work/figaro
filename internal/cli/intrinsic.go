package cli

import "strings"

// Form address `<host>/<intrinsic>`: `state` is the host's own form and is implied;
// for a role that is the role form's own state, not its target's.
const (
	intrinsicState   = "state"
	intrinsicRuntime = "runtime"
	intrinsicQueue   = "queue"
)

// splitIntrinsic returns "" for the identity segment.
func splitIntrinsic(spec string) (host, intrinsic string) {
	i := strings.LastIndexByte(spec, '/')
	if i < 0 {
		return spec, ""
	}
	host, name := spec[:i], spec[i+1:]
	if name == intrinsicState {
		return host, ""
	}
	return host, name
}

func knownIntrinsic(name string) bool {
	switch name {
	case "", intrinsicState, intrinsicRuntime, intrinsicQueue:
		return true
	}
	return false
}

func intrinsicNames() []string { return []string{intrinsicRuntime, intrinsicQueue} }

func formAddress(host, intrinsic string) string {
	if intrinsic == "" || intrinsic == intrinsicState {
		return host
	}
	return host + "/" + intrinsic
}
