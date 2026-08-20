package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

var (
	nullBytes      = []byte("null")
	openBracket    = []byte("[")
	closeBracket   = []byte("]")
	commaSeparator = []byte(",")
)

// WriteJSONWithRows writes the bytes json.Marshal would produce for frame with
// rows in the named field, without ever holding them in one buffer. frame must
// carry that field as a nil slice; the field must not be omitempty.
//
// Each tenant asserts the equality against its own request type -- see
// TestStreamedBodyIsByteIdenticalToMarshal in anthropic and openaichat.
func WriteJSONWithRows(w io.Writer, frame any, field string, rows []json.RawMessage) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal request frame: %w", err)
	}
	name := []byte(`"` + field + `":`)
	token := append(append([]byte{}, name...), nullBytes...)
	i := bytes.Index(encoded, token)
	if i < 0 {
		return fmt.Errorf("request frame carries no null %q field", field)
	}
	head, tail := encoded[:i+len(name)], encoded[i+len(token):]

	if _, err := w.Write(head); err != nil {
		return err
	}
	if rows == nil {
		if _, err := w.Write(nullBytes); err != nil {
			return err
		}
		_, err = w.Write(tail)
		return err
	}
	if _, err := w.Write(openBracket); err != nil {
		return err
	}
	var compacted, escaped bytes.Buffer
	for n, row := range rows {
		if n > 0 {
			if _, err := w.Write(commaSeparator); err != nil {
				return err
			}
		}
		compacted.Reset()
		if err := json.Compact(&compacted, row); err != nil {
			return fmt.Errorf("row %d: %w", n, err)
		}
		escaped.Reset()
		json.HTMLEscape(&escaped, compacted.Bytes())
		if _, err := w.Write(escaped.Bytes()); err != nil {
			return err
		}
	}
	if _, err := w.Write(closeBracket); err != nil {
		return err
	}
	_, err = w.Write(tail)
	return err
}
