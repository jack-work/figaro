package tool

// bashToolEnv returns the extra env vars every bash tool invocation
// gets appended to its environment. The primary purpose is to prevent
// an aria's shell-outs from silently inheriting the daemon shell's
// aria binding: nested `figaro` calls should treat their target ids
// as absolute (via --id) and never mutate the parent's binding state.
//
// FIGARO_NO_BIND=1 is respected by every binding-touching CLI verb.
func bashToolEnv() []string {
	return []string{"FIGARO_NO_BIND=1"}
}
