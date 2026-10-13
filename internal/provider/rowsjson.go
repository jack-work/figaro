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

// RowSeq pulls wire rows one at a time with their figaro LTs. It must be
// re-runnable: net/http replays a body on a GOAWAY or a 307.
type RowSeq = iter.Seq2[json.RawMessage, uint64]

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

// PerMessageRows flattens records, each of one or more wire messages.
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

// CollectRows materializes the conversation; nothing on the anthropic send
// path may call it.
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

// WriteJSONWithRows writes what json.Marshal would, with rows spliced into
// the named field. frame must carry that field as a nil, non-omitempty slice.
func WriteJSONWithRows(w io.Writer, frame any, field string, rows []json.RawMessage) error {
	if rows == nil {
		return WriteJSONWithRowSeq(w, frame, field, nil)
	}
	return WriteJSONWithRowSeq(w, frame, field, SliceRows(rows, nil))
}
