package form

// mustEntry is the value a patch sets at key, for tests that assert on one.
func mustEntry(p Patch, key string) []byte {
	e, ok := p.Entry(key)
	if !ok {
		return nil
	}
	return e.New
}
