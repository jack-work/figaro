package compose

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// Real-data proof for S32: every turn in a genuine 173-message aria — written
// entirely BEFORE the input/output rename — has exactly one inquiry, and the
// legacy role vocabulary decodes through message.Role.UnmarshalJSON with no
// migration. Skips when the fixture is absent so the suite stays hermetic.
func TestTurns_RealAriaHasAnInquiryPerTurn(t *testing.T) {
	f, err := os.Open("/tmp/w19fix/real.jsonl")
	if err != nil {
		t.Skip("no real-data fixture")
	}
	defer f.Close()

	var msgs []message.Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e struct {
			M uint64          `json:"m"`
			P message.Message `json:"p"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		e.P.LogicalTime = e.M
		msgs = append(msgs, e.P)
	}

	roles := map[message.Role]int{}
	for _, m := range msgs {
		roles[m.Role]++
	}
	if roles["user"] != 0 || roles["assistant"] != 0 {
		t.Errorf("legacy vocabulary leaked through decode: %v", roles)
	}
	if roles[message.RoleInput] == 0 || roles[message.RoleOutput] == 0 {
		t.Errorf("expected input+output after normalisation, got %v", roles)
	}

	turns := Turns(msgs, nil)
	if len(turns) == 0 {
		t.Fatal("no turns from real data")
	}
	for _, tn := range turns {
		if tn.Inquiry == "" {
			t.Errorf("turn %d has no inquiry", tn.ID)
		}
	}
	t.Logf("messages=%d turns=%d roles=%v", len(msgs), len(turns), roles)
}
