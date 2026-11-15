package form

import (
	"encoding/json"
	"slices"
	"strings"
)

const StudyKeysKey = "system.study_keys"
const KeyLibrettoAlive = "system.libretto.alive"

func StudyKeys(snap interface {
	Get(string) (json.RawMessage, bool)
}, sourceFormID string) []string {
	raw, ok := snap.Get(StudyKeysKey + "." + sourceFormID)
	if !ok {
		return nil
	}
	var keys []string
	if json.Unmarshal(raw, &keys) != nil {
		return nil
	}
	return NormalizeStudyKeys(keys)
}

func NormalizeStudyKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		k = strings.Trim(strings.TrimSpace(k), ".")
		if k != "" {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func StudyKeyShown(key string, keys []string) bool {
	if len(keys) == 0 {
		return !strings.HasPrefix(key, "system.")
	}
	for _, p := range keys {
		if key == p || strings.HasPrefix(key, p+".") {
			return true
		}
	}
	return false
}
