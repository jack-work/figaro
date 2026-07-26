package tool

// bashToolEnv returns the extra env vars every bash tool invocation
// gets appended to its environment.
//
// Two knobs, deliberately separate:
//
//   - FIGARO_ARIA=<id> is the aria's *identity*. A shell-out is
//     statically attended to the aria that spawned it, so `figaro
//     state`, `figaro set`, `figaro status` and a bare `figaro send`
//     all target that aria with no --id. Empty ariaID omits the var
//     (tests and trivial callers with no aria in hand).
//   - FIGARO_NO_BIND=1 forbids *mutating* the daemon's pid→aria map,
//     so an aria's shell-outs never inherit or clobber the binding of
//     the terminal that started the daemon. It is what makes the
//     identity above static: `figaro attend` refuses here.
//
// The pair is the whole model — an aria knows who it is and cannot
// become someone else. Talking to another aria takes an explicit --id.
func bashToolEnv(ariaID string) []string {
	env := []string{
		"FIGARO_NO_BIND=1",
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"EDITOR=true",
	}
	if ariaID != "" {
		env = append(env, "FIGARO_ARIA="+ariaID)
	}
	return env
}
