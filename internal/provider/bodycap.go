package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"

	"github.com/jack-work/figaro/api/form"
)

const MaxRequestBytesKey = "system.max_request_bytes"

func MaxRequestBytes(snapshot form.Snapshot, routeDefault int) int {
	raw, ok := snapshot.Get(MaxRequestBytesKey)
	if !ok {
		return routeDefault
	}
	var n int
	if json.Unmarshal(raw, &n) == nil && n >= 0 {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return routeDefault
}

type ImageDialect struct {
	Is   func(part map[string]any) bool
	Text func(note string) map[string]any

	data     func(part map[string]any) (wireImage, bool)
	withData func(part map[string]any, img wireImage) map[string]any
}

var (
	MessagesImages = ImageDialect{
		Is:       func(p map[string]any) bool { return p["type"] == "image" },
		Text:     func(note string) map[string]any { return map[string]any{"type": "text", "text": note} },
		data:     messagesImageData,
		withData: messagesWithImageData,
	}
	ChatImages = ImageDialect{
		Is:       func(p map[string]any) bool { return p["type"] == "image_url" },
		Text:     func(note string) map[string]any { return map[string]any{"type": "text", "text": note} },
		data:     chatImageData,
		withData: chatWithImageData,
	}
	ResponsesImages = ImageDialect{
		Is:       func(p map[string]any) bool { return p["type"] == "input_image" },
		Text:     func(note string) map[string]any { return map[string]any{"type": "input_text", "text": note} },
		data:     responsesImageData,
		withData: responsesWithImageData,
	}
)

type RequestTooLarge struct {
	Provider string
	Bytes    int
	Limit    int
	Elided   int
}

func (e *RequestTooLarge) Error() string {
	after := ""
	if e.Elided > 0 {
		after = fmt.Sprintf(" after eliding all %d images", e.Elided)
	}
	return fmt.Sprintf("%s: request too large: %s%s, over the limit of %s (%s); fork or succeed the aria to send less",
		e.Provider, humanBytes(e.Bytes), after, humanBytes(e.Limit), MaxRequestBytesKey)
}

func humanBytes(n int) string {
	return fmt.Sprintf("%.2f MiB (%d bytes)", float64(n)/(1<<20), n)
}

type countingWriter struct{ n int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

func BodySize(body BodyFunc) (int, error) {
	var c countingWriter
	if err := body(&c); err != nil {
		return 0, err
	}
	return c.n, nil
}

func ShapeRequest(label string, rows RowSeq, in SendInput, routeLimit int, dialect ImageDialect, body func(RowSeq) BodyFunc) (RowSeq, error) {
	if in.TextOnly != "" {
		rows = ElideImages(rows, dialect, "[image omitted: "+in.TextOnly+"]")
	}
	return FitRequest(label, rows, MaxRequestBytes(in.Snapshot, routeLimit), dialect, body)
}

func ElideImages(rows RowSeq, dialect ImageDialect, note string) RowSeq {
	return func(yield func(json.RawMessage, uint64) bool) {
		for row, lt := range rows {
			if bytes.Contains(row, []byte("image")) {
				for {
					smaller, ok := elideOneImage(row, dialect, note)
					if !ok {
						break
					}
					row = smaller
				}
			}
			if !yield(row, lt) {
				return
			}
		}
	}
}

func FitRequest(provider string, rows RowSeq, limit int, dialect ImageDialect, body func(RowSeq) BodyFunc) (RowSeq, error) {
	if limit <= 0 {
		return rows, nil
	}
	total, err := BodySize(body(rows))
	if err != nil || total <= limit {
		return rows, err
	}
	list, lts := CollectRows(rows)
	if total, err = downscaleOldest(provider, list, lts, total, limit, dialect, body); err != nil || total <= limit {
		return SliceRows(list, lts), err
	}
	note := fmt.Sprintf("[image elided: the request was over %s's %s request limit, so figaro dropped the oldest images first]", provider, humanBytes(limit))
	elided, i := 0, 0
	for total > limit {
		before := elided
		for ; i < len(list) && total > limit; i++ {
			for total > limit {
				smaller, ok := elideOneImage(list[i], dialect, note)
				if !ok {
					break
				}
				total -= len(list[i]) - len(smaller)
				list[i] = smaller
				elided++
			}
			if total <= limit {
				break
			}
		}
		if total, err = BodySize(body(SliceRows(list, lts))); err != nil {
			return nil, err
		}
		if elided == before || i >= len(list) {
			break
		}
	}
	if total > limit {
		return nil, &RequestTooLarge{Provider: provider, Bytes: total, Limit: limit, Elided: elided}
	}
	fitted := SliceRows(list, lts)
	slog.Warn("request over the provider's limit; the oldest images were elided", "provider", provider, "elided", elided, "bytes", total, "limit", limit)
	return fitted, nil
}

func downscaleOldest(provider string, list []json.RawMessage, lts []uint64, total, limit int, dialect ImageDialect, body func(RowSeq) BodyFunc) (int, error) {
	if dialect.data == nil {
		return total, nil
	}
	shrunk := 0
	for i := 0; i < len(list) && total > limit; i++ {
		if !bytes.Contains(list[i], []byte("image")) {
			continue
		}
		v, ok := decodeRow(list[i])
		if !ok {
			continue
		}
		over := total - limit
		changed := rewriteImages(&v, dialect, func(part map[string]any) []any {
			if over <= 0 {
				return nil
			}
			img, ok := dialect.data(part)
			if !ok {
				return nil
			}
			target := len(img.Data) - over
			smaller, fitted, ok := downscaleImage(img, target)
			if !ok {
				return nil
			}
			over -= len(img.Data) - len(smaller.Data)
			shrunk++
			why := fmt.Sprintf("the request was over %s's %s request limit, so figaro sent the oldest images smaller first", provider, humanBytes(limit))
			return []any{dialect.Text(downscaleNote(why, fitted)), dialect.withData(part, smaller)}
		})
		if !changed {
			continue
		}
		out, err := json.Marshal(v)
		if err != nil {
			continue
		}
		total += len(out) - len(list[i])
		list[i] = out
	}
	if shrunk == 0 {
		return total, nil
	}
	total, err := BodySize(body(SliceRows(list, lts)))
	if err == nil {
		slog.Warn("request over the provider's limit; the oldest images were downscaled", "provider", provider, "downscaled", shrunk, "bytes", total, "limit", limit)
	}
	return total, err
}

func elideOneImage(row json.RawMessage, dialect ImageDialect, note string) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(row))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return row, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return row, false
	}
	if !replaceFirstImage(&v, dialect, note) {
		return row, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return row, false
	}
	return out, true
}

func replaceFirstImage(v *any, dialect ImageDialect, note string) bool {
	switch x := (*v).(type) {
	case map[string]any:
		if dialect.Is(x) {
			*v = dialect.Text(note)
			return true
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := x[k]
			if replaceFirstImage(&child, dialect, note) {
				x[k] = child
				return true
			}
		}
	case []any:
		for i := range x {
			if replaceFirstImage(&x[i], dialect, note) {
				return true
			}
		}
	}
	return false
}
