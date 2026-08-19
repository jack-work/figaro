package crashtest

type Rec struct {
	ChanLT  uint64
	MainLT  uint64
	Payload []byte
}

const (
	chanMain  = "main"
	chanNotes = "notes"
	chanState = "state"
)

var allChans = []string{chanMain, chanNotes, chanState}

type Store interface {
	Trunks() ([]string, error)
	AppendMain(trunk string, payload []byte) (uint64, error)
	AppendChannel(trunk, channel string, mainLT uint64, payload []byte) (uint64, error)
	Kick()
	ForkAt(trunk string, atMainLT uint64) (string, error)
	// Detach makes a trunk self-sufficient: it absorbs the history prefix
	// it reads through an ancestor and stops pointing at one. Crash-safe by
	// ordering alone, which is a claim only this harness can test.
	Detach(trunk string) error
	ReadAll(trunk, channel string) ([]Rec, error)
	TailRecord(trunk, channel string) (Rec, bool, error)
	MainTail(trunk string) (uint64, error)
	State(trunk string) ([]byte, error)
	Close() error
}
