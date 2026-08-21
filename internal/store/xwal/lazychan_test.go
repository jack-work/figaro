package xwal

import "github.com/jack-work/figaro/internal/store/log"

// mustLog is the tests' reach into a channel's log, opening it if it is not
// open. A test that cannot open a channel has no business continuing.
func (ch *channel) mustLog() *log.Log {
	l, err := ch.Log()
	if err != nil {
		panic(err)
	}
	return l
}
