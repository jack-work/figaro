package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"iter"
)

var (
	nullBytes      = []byte("null")
	openBracket    = []byte("[")
	closeBracket   = []byte("]")
	commaSeparator = []byte(",")
)

// RowSeq is a conversation's wire rows, pulled one at a time, each with the
// figaro logical time of the record it translates.
//
// IT IS THE POINT OF THE STREAMED BODY. A []json.RawMessage of the whole
// history is the conversation decoded into the heap and held until the request
// ends; a RowSeq is the same rows in the same order with one of them resident.
// The sequence must be RE-RUNNABLE: net/http replays a body on a GOAWAY or a
// 307, so a producer reads its source again rather than remembering it.
type RowSeq = iter.Seq2[json.RawMessage, uint64]

// SliceRows is the RowSeq over rows already in memory: for tests, for the
// assemblers that have no log, and as the definition every streamed source is
// asserted equal to.
func SliceRows(rows []json.RawMessage, lts []uint64) RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		for i, row := range rows {
			var lt uint64
			if i < len(lts) {
				lt = lts[i]
			}
			if !yield(row, lt) {
				return
			}
		}
	}
}

// PerMessageRows flattens stored rows -- one record translates to one or more
// wire messages -- into the sequence an assembler consumes.
func PerMessageRows(perMessage [][]json.RawMessage, lts []uint64) RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		for i, entry := range perMessage {
			var lt uint64
			if i < len(lts) {
				lt = lts[i]
			}
			for _, raw := range entry {
				if len(raw) == 0 {
					continue
				}
				if !yield(raw, lt) {
					return
				}
			}
		}
	}
}

// CollectRows drains a RowSeq. THE ONE PLACE THE WHOLE CONVERSATION IS
// ALLOWED IN MEMORY: tests that assert on an assembled request, and callers
// whose wire format is a typed array rather than a splice. Nothing on the
// anthropic send path may call it.
func CollectRows(seq RowSeq) ([]json.RawMessage, []uint64) {
	var (
		rows []json.RawMessage
		lts  []uint64
	)
	if seq == nil {
		return nil, nil
	}
	for row, lt := range seq {
		rows = append(rows, row)
		lts = append(lts, lt)
	}
	return rows, lts
}

// WriteJSONWithRowSeq is WriteJSONWithRows over a pulled sequence: the same
// bytes, with one row resident instead of all of them.
func WriteJSONWithRowSeq(w io.Writer, frame any, field string, rows RowSeq) error {
	head, tail, err := spliceFrame(frame, field)
	if err != nil {
		return err
	}
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
	var (
		compacted, escaped bytes.Buffer
		n                  int
		werr               error
	)
	for row := range rows {
		if n > 0 {
			if _, werr = w.Write(commaSeparator); werr != nil {
				break
			}
		}
		compacted.Reset()
		if werr = json.Compact(&compacted, row); werr != nil {
			werr = fmt.Errorf("row %d: %w", n, werr)
			break
		}
		escaped.Reset()
		json.HTMLEscape(&escaped, compacted.Bytes())
		if _, werr = w.Write(escaped.Bytes()); werr != nil {
			break
		}
		n++
	}
	if werr != nil {
		return werr
	}
	if _, err := w.Write(closeBracket); err != nil {
		return err
	}
	_, err = w.Write(tail)
	return err
}

// spliceFrame marshals the frame and cuts it at the null field the rows
// replace.
func spliceFrame(frame any, field string) (head, tail []byte, err error) {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request frame: %w", err)
	}
	name := []byte(`"` + field + `":`)
	token := append(append([]byte{}, name...), nullBytes...)
	i := bytes.Index(encoded, token)
	if i < 0 {
		return nil, nil, fmt.Errorf("request frame carries no null %q field", field)
	}
	return encoded[: i+len(name) : i+len(name)], encoded[i+len(token):], nil
}

// WriteJSONWithRows writes the bytes json.Marshal would produce for frame with
// rows in the named field, without ever holding them in one buffer. frame must
// carry that field as a nil slice; the field must not be omitempty.
//
// Each tenant asserts the equality against its own request type -- see
// TestStreamedBodyIsByteIdenticalToMarshal in anthropic and openaichat.
func WriteJSONWithRows(w io.Writer, frame any, field string, rows []json.RawMessage) error {
	if rows == nil {
		// A nil field is the literal null, which is not the empty array.
		return WriteJSONWithRowSeq(w, frame, field, nil)
	}
	return WriteJSONWithRowSeq(w, frame, field, SliceRows(rows, nil))
}
