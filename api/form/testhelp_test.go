package form

import "encoding/json"

// mustEntry is the value a patch sets at key.
func mustEntry(p Patch, key string) json.RawMessage {
	e, ok := p.Entry(key)
	if !ok {
		return nil
	}
	return e.New
}
