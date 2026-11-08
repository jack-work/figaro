package figaro

import "time"

func SetStreamCutBackoffForTest(d ...time.Duration) func() {
	prev := streamCutBackoff
	streamCutBackoff = d
	return func() { streamCutBackoff = prev }
}
