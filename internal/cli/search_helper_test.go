package cli

// findQuery compiles q and runs the search — test convenience for the many
// literal-query tests that predate compiled patterns.
func (t *transcript) findQuery(q string) {
	if p, err := compileSearch(q); err == nil {
		t.find(p, 1)
	}
}
