package store

import (
	"encoding/json"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

func studyKeysPath(sourceFormID string) string { return form.StudyKeysKey + "." + sourceFormID }

func onlyStudyKeys(snap interface {
	Get(string) (json.RawMessage, bool)
}, sourceFormID string) bool {
	raw, ok := snap.Get(form.StudyKeysKey)
	if !ok {
		return false
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(raw, &all) != nil {
		return false
	}
	_, held := all[sourceFormID]
	return held && len(all) == 1
}

func describeKeys(keys []string) string {
	if len(keys) == 0 {
		return "(all)"
	}
	return strings.Join(keys, ",")
}
