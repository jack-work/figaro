package form

import "strings"

var ownerHeldKeys = []string{
	"system.provider",
	"system.model",
	"system.max_tokens",
	"system.max_context_tokens",
	"system.credo",
	"system.tools",
	"system.methods",
	"system.environment",
	"system.decide.base_url",
	"system.ttl",
	"system.reap_if_empty",
}

func OwnerHeldKeys() []string { return append([]string(nil), ownerHeldKeys...) }

func OwnerHeld(path string) (string, bool) {
	for _, k := range ownerHeldKeys {
		if path == k || strings.HasPrefix(path, k+".") || strings.HasPrefix(path, k+"[") {
			return k, true
		}
	}
	return "", false
}
