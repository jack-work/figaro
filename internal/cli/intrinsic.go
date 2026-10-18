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
	switch {
	case name == intrinsicState:
		return host, ""
	case knownIntrinsic(name), !looksLikeID(name):
		return host, name
	}
	return spec, ""
}

func looksLikeID(s string) bool {
	if i := strings.IndexAny(s, ":."); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "@")
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
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
