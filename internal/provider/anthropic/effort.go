package anthropic

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/turns"
)

const betaMessageEffort = "mid-conversation-output-config-2026-07-01"

type effortRow struct {
	Role         string        `json:"role"`
	Content      []nativeBlock `json:"content"`
	OutputConfig outputConfig  `json:"output_config"`
}

func effortRowFor(msg message.Message, board form.Snapshot) (json.RawMessage, bool) {
	if msg.Role != message.RoleInput || !turns.Opens(msg) {
		return nil, false
	}
	effort := board.Lookup(provider.ThinkingEffortKey)
	if effort == nil || *effort == "" {
		return nil, false
	}
	raw, err := json.Marshal(effortRow{Role: "system", Content: []nativeBlock{}, OutputConfig: outputConfig{Effort: *effort}})
	if err != nil {
		return nil, false
	}
	return raw, true
}

var outputConfigToken = []byte(`"output_config"`)

func isEffortRow(row json.RawMessage) bool {
	return bytes.Contains(row, outputConfigToken) && rowRole(row) == "system"
}

func dropEffortRowsSeq(in provider.RowSeq) provider.RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		in(func(row json.RawMessage, lt uint64) bool {
			if isEffortRow(row) {
				return true
			}
			return yield(row, lt)
		})
	}
}

func (a *Anthropic) KeepsCacheOnChange(key string, snap form.Snapshot) bool {
	return key == provider.ThinkingEffortKey && provider.SupportsMessageEffort(a.resolveModel(snap))
}

func messageBetas(model string) []string {
	if !provider.SupportsMessageEffort(model) {
		return nil
	}
	return []string{betaMessageEffort}
}

func betaHeader(betas []string) http.Header {
	if len(betas) == 0 {
		return nil
	}
	return http.Header{"Anthropic-Beta": {strings.Join(betas, ",")}}
}
