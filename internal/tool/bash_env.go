package tool

// bashToolEnv returns the extra env vars every bash tool invocation
// gets appended to its environment.
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
