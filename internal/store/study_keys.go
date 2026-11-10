package store

import (
	"encoding/json"
	"slices"
	"strings"
)

const StudyKeysKey = "system.study_keys"

func studyKeysPath(sourceFormID string) string {
	return StudyKeysKey + "." + sourceFormID
}

func StudyKeys(snap interface {
	Get(string) (json.RawMessage, bool)
}, sourceFormID string) []string {
	raw, ok := snap.Get(studyKeysPath(sourceFormID))
	if !ok {
		return nil
	}
	var keys []string
	if json.Unmarshal(raw, &keys) != nil {
		return nil
	}
	return NormalizeStudyKeys(keys)
}

func onlyStudyKeys(snap interface {
	Get(string) (json.RawMessage, bool)
}, sourceFormID string) bool {
	raw, ok := snap.Get(StudyKeysKey)
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

func describeKeys(keys []string) string {
	if len(keys) == 0 {
		return "(all)"
	}
	return strings.Join(keys, ",")
}
