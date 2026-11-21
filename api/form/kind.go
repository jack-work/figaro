package form

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	KindKey = "system.kind"
	KindLog = "log"
)

var ErrKindFixed = errors.New(KindKey + " is fixed at birth: fork with -S " + KindKey + "=... for another kind")

var logModelKeys = []string{"system.provider", "system.model"}

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

func CheckKind(before, after Snapshot, birth bool) error {
	if !birth && KindOf(before) != KindOf(after) {
		return ErrKindFixed
	}
	if !IsLog(after) {
		return nil
	}
	for _, k := range logModelKeys {
		if after.Has(k) {
			return fmt.Errorf("an aria log has no model, so it carries no %s: drop the key (-D %s at a fork), or mint the log with form.bind (figaro new --log)", k, k)
		}
	}
	return nil
}
