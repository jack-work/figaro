package crashtest

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
)

type payload struct {
	G string `json:"g"`
	C string `json:"c"`
	Q uint64 `json:"q"`
	M uint64 `json:"m"`
	X string `json:"x"`
}

func checksum(g, c string, q, m uint64, salt string) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%s|%d|%d|%s", g, c, q, m, salt)
	return fmt.Sprintf("%016x", h.Sum64())
}

func encodePayload(g, c string, q, m uint64, salt string) []byte {
	b, _ := json.Marshal(payload{G: g, C: c, Q: q, M: m, X: checksum(g, c, q, m, salt)})
	return b
}

func decodePayload(b []byte, salt string) (p payload, ours, valid bool) {
	if err := json.Unmarshal(b, &p); err != nil || p.G == "" || p.X == "" {
		return payload{}, false, false
	}
	return p, true, p.X == checksum(p.G, p.C, p.Q, p.M, salt)
}

func recHash(mainLT uint64, payload []byte) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|", mainLT)
	h.Write(payload)
	return h.Sum64()
}
