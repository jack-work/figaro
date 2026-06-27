package store

import "time"

// AriaInfo is persisted aria metadata for `figaro list`.
type AriaInfo struct {
	ID           string
	MessageCount int
	LastModified time.Time
	Meta         *AriaMeta // nil if no meta.json
}
