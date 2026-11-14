package responses

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOmissionOnlyRemovesConfiguredTopLevelFields(t *testing.T) {
	body := responseStreamRequest{responseBody: responseBody{Model: "model", Input: []json.RawMessage{json.RawMessage(`{"type":"function_call","arguments":"{\"max_output_tokens\":7}"}`)}, MaxOutputTokens: 321}, Stream: true}
	for _, omit := range [][]string{nil, {"max_output_tokens"}} {
		p := &Provider{omitFields: omit}
		raw, err := p.marshalRequest(body)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &fields))
		_, present := fields["max_output_tokens"]
		require.Equal(t, len(omit) == 0, present)
		require.JSONEq(t, `false`, string(fields["store"]))
		require.JSONEq(t, `true`, string(fields["stream"]))
		require.Contains(t, string(fields["input"]), `max_output_tokens`)
	}
}

func TestTransportFieldsCannotBeOmitted(t *testing.T) {
	for _, field := range []string{"model", "input", "stream", "store", "type", "headers", "agent_task_id", "initiator", "input.tools", "", " max_output_tokens"} {
		require.Error(t, ValidateOmitFields([]string{field}))
	}
}
