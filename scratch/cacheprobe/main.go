package main

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/store"
)

func main() {
	be, err := store.NewXwalBackend("/var/tmp/figcache-ab/state/arias", 0)
	if err != nil {
		fmt.Println("backend:", err)
		return
	}
	defer be.Close()
	for _, ns := range []string{"anthropic", "copilot-messages", "copilot-responses"} {
		lg, err := be.OpenTranslation("64e300d9", ns)
		if err != nil {
			fmt.Printf("%-18s open err: %v\n", ns, err)
			continue
		}
		before := len(lg.Read())
		_, aerr := lg.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: 99, Payload: []json.RawMessage{json.RawMessage(`{"probe":1}`)}, Fingerprint: "probe",
		})
		fmt.Printf("%-18s rows before=%d append err=%v rows after=%d\n", ns, before, aerr, len(lg.Read()))
	}
}
