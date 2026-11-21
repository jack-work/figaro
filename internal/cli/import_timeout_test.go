package cli

import (
	"testing"
	"time"
)

func TestAnImportOfManyMessagesGetsTimeToAppendThemAll(t *testing.T) {
	const appendCost = 4 * time.Millisecond
	for _, n := range []int{1, 1000, 130000} {
		if need := time.Duration(n) * appendCost; importTimeout(n) < need {
			t.Errorf("%d messages need %s to append, the import allows %s", n, need, importTimeout(n))
		}
	}
}
