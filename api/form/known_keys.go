package form

// KeyMode classifies a known key by who writes it.
type KeyMode int

const (
	KeyUserSettable KeyMode = iota
	// KeySystemManaged keys are written by the harness and may be
	// overwritten if set by hand.
	KeySystemManaged
	// KeyEphemeralPerTurn keys are rewritten on every prompt.
	KeyEphemeralPerTurn
)

type KeyDoc struct {
	// Key is the dotted path; a "<name>" segment is a placeholder.
	Key   string
	Short string
	Mode  KeyMode
}

func WellKnownKeys() []KeyDoc {
	var out []KeyDoc
	for _, s := range registry {
		if s.Short == "" {
			continue
		}
		out = append(out, KeyDoc{Key: s.Path, Short: s.Short, Mode: s.mode()})
	}
	return out
}

func (s Spec) mode() KeyMode {
	switch {
	case s.Writer == WriterRuntime:
		return KeySystemManaged
	case s.Ephemeral:
		return KeyEphemeralPerTurn
	}
	return KeyUserSettable
}

// IntrinsicKeys catalogs a non-persistent builtin form addressed as
// `<host>/<name>`; no set reaches these keys.
func IntrinsicKeys(intrinsic string) []KeyDoc {
	switch intrinsic {
	case "runtime":
		return []KeyDoc{
			{Key: "turn.state", Short: `What the aria is DOING: "idle", "accepted", "committing", "thinking", "tooling"`, Mode: KeySystemManaged},
			{Key: "turn.id", Short: "The in-flight turn id", Mode: KeySystemManaged},
			{Key: "turn.since", Short: "Unix millis of the last transition", Mode: KeySystemManaged},
			{Key: "turn.reason", Short: "The verdict of the turn that just ended; carried only into idle", Mode: KeySystemManaged},
			{Key: "inflight", Short: "Messages accepted and not yet answered", Mode: KeySystemManaged},
			{Key: "epoch", Short: "The inbox generation queue ids belong to", Mode: KeySystemManaged},
			{Key: "model", Short: "Active model id", Mode: KeySystemManaged},
		}
	case "queue":
		return []KeyDoc{
			{Key: "epoch", Short: "The generation these ids belong to; they restart with it", Mode: KeySystemManaged},
			{Key: "len", Short: "How many messages are in flight", Mode: KeySystemManaged},
			{Key: "order", Short: "FIFO order, as ids. A form is a map and a map has none", Mode: KeySystemManaged},
			{Key: "items.<id>.text", Short: "The message, verbatim", Mode: KeySystemManaged},
			{Key: "items.<id>.sender", Short: "Who asked", Mode: KeySystemManaged},
			{Key: "items.<id>.state", Short: `Where it is: "queued", "committing", "committed", "merged", "dropped", "drained"`, Mode: KeySystemManaged},
			{Key: "items.<id>.at", Short: "Accepted-at, unix millis", Mode: KeySystemManaged},
			{Key: "items.<id>.merged", Short: "Ids an interrupt folded INTO this message", Mode: KeySystemManaged},
			{Key: "items.<id>.into", Short: "For a merged message, the surviving id", Mode: KeySystemManaged},
			{Key: "items.<id>.turn", Short: "For a committed message, the turn it became", Mode: KeySystemManaged},
		}
	}
	return nil
}

func IntrinsicNames() []string { return []string{"runtime", "queue"} }
