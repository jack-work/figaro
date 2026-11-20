package form

import "encoding/json"

const (
	KindKey = "system.kind"
	KindLog = "log"
)

func KindOf(g KeyGetter) string {
	raw, ok := g.Get(KindKey)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func IsLog(g KeyGetter) bool { return KindOf(g) == KindLog }
