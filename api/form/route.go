package form

import "strings"

var RouteKeys = []string{"system.provider", "system.model", "system.thinking_effort", "system.thinking_budget"}

func IsRouteKey(key string) bool {
	for _, k := range RouteKeys {
		if key == k || strings.HasPrefix(key, k+".") {
			return true
		}
	}
	return false
}
