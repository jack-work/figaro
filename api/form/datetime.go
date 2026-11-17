package form

import (
	"encoding/json"
	"time"
)

const DatetimeKey = "datetime"

func Datetime(t time.Time) json.RawMessage {
	b, _ := json.Marshal(t.Format("Monday, January 2, 2006, 3PM MST"))
	return b
}
