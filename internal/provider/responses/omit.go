package responses

import (
	"encoding/json"
	"fmt"
	"strings"
)

func ValidateOmitFields(fields []string) error {
	for _, field := range fields {
		if field == "" || strings.TrimSpace(field) != field || strings.ContainsAny(field, ".[]") {
			return fmt.Errorf("responses omit_fields: %q is not a top-level field", field)
		}
		switch field {
		case "model", "input", "stream", "store", "type", "headers", "agent_task_id", "initiator":
			return fmt.Errorf("responses omit_fields: %q is required by the transport", field)
		}
	}
	return nil
}

func (p *Provider) marshalRequest(body any) ([]byte, error) {
	if err := ValidateOmitFields(p.omitFields); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil || len(p.omitFields) == 0 {
		return raw, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for _, field := range p.omitFields {
		delete(fields, field)
	}
	return json.Marshal(fields)
}
