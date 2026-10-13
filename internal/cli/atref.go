package cli

// refSigil is only the completion side; `@key!` expansion runs on the daemon.
var refSigil byte = '@'

func SetRefSigil(s string) {
	if len(s) == 1 && (s[0] == '@' || s[0] == ':') {
		refSigil = s[0]
	}
}
