package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/tool"
)

type wireImage struct {
	Data string
	Mime string
}

func messagesImageData(p map[string]any) (wireImage, bool) {
	src, ok := p["source"].(map[string]any)
	if !ok || src["type"] != "base64" {
		return wireImage{}, false
	}
	data, _ := src["data"].(string)
	mime, _ := src["media_type"].(string)
	return wireImage{Data: data, Mime: mime}, data != ""
}

func messagesWithImageData(p map[string]any, img wireImage) map[string]any {
	src, _ := p["source"].(map[string]any)
	next := make(map[string]any, len(src))
	for k, v := range src {
		next[k] = v
	}
	next["data"], next["media_type"] = img.Data, img.Mime
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	out["source"] = next
	return out
}

func parseDataURL(u string) (wireImage, bool) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return wireImage{}, false
	}
	mime, data, ok := strings.Cut(rest, ";base64,")
	if !ok || data == "" {
		return wireImage{}, false
	}
	return wireImage{Data: data, Mime: mime}, true
}

func dataURLOf(img wireImage) string { return "data:" + img.Mime + ";base64," + img.Data }

func chatImageData(p map[string]any) (wireImage, bool) {
	u, ok := p["image_url"].(map[string]any)
	if !ok {
		return wireImage{}, false
	}
	s, _ := u["url"].(string)
	return parseDataURL(s)
}

func chatWithImageData(p map[string]any, img wireImage) map[string]any {
	u, _ := p["image_url"].(map[string]any)
	next := make(map[string]any, len(u))
	for k, v := range u {
		next[k] = v
	}
	next["url"] = dataURLOf(img)
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	out["image_url"] = next
	return out
}

func responsesImageData(p map[string]any) (wireImage, bool) {
	s, _ := p["image_url"].(string)
	return parseDataURL(s)
}

func responsesWithImageData(p map[string]any, img wireImage) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	out["image_url"] = dataURLOf(img)
	return out
}

func decodeRow(row json.RawMessage) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(row))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

func rewriteImages(v *any, dialect ImageDialect, fn func(part map[string]any) []any) bool {
	changed := false
	switch x := (*v).(type) {
	case map[string]any:
		if dialect.Is(x) {
			if out := fn(x); len(out) == 1 {
				*v = out[0]
				return true
			}
			return false
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := x[k]
			if rewriteImages(&child, dialect, fn) {
				x[k] = child
				changed = true
			}
		}
	case []any:
		next := make([]any, 0, len(x))
		for i := range x {
			if part, ok := x[i].(map[string]any); ok && dialect.Is(part) {
				if out := fn(part); out != nil {
					next = append(next, out...)
					changed = true
					continue
				}
				next = append(next, part)
				continue
			}
			if rewriteImages(&x[i], dialect, fn) {
				changed = true
			}
			next = append(next, x[i])
		}
		if changed {
			*v = next
		}
	}
	return changed
}

func downscaleImage(img wireImage, maxBase64 int) (wireImage, tool.FittedImage, bool) {
	if maxBase64 <= 0 {
		return wireImage{}, tool.FittedImage{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return wireImage{}, tool.FittedImage{}, false
	}
	f, err := tool.FitImage(raw, img.Mime, tool.ImageLimits{MaxBase64: maxBase64, MaxDim: tool.DefaultMaxSendDim})
	if err != nil || len(f.Data) >= len(img.Data) {
		return wireImage{}, tool.FittedImage{}, false
	}
	return wireImage{Data: f.Data, Mime: f.MimeType}, f, true
}

func downscaleNote(why string, f tool.FittedImage) string {
	if note := f.Note(); note != "" {
		return "[image downscaled: " + why + ". " + strings.TrimSuffix(strings.TrimPrefix(note, "["), "]") + "]"
	}
	return "[image downscaled: " + why + "]"
}

func FitImages(rows RowSeq, lim ImageLimits, dialect ImageDialect) RowSeq {
	if (lim.MaxBytes <= 0 && lim.MaxCount <= 0) || dialect.data == nil {
		return rows
	}
	list, lts := CollectRows(rows)
	drop := 0
	if lim.MaxCount > 0 {
		drop = countImages(list, dialect) - lim.MaxCount
	}
	maxBase64 := 0
	if lim.MaxBytes > 0 {
		maxBase64 = base64.StdEncoding.EncodedLen(lim.MaxBytes)
	}
	countNote := dialect.Text("[image elided: " + lim.CountWhy + ", so figaro dropped the oldest]")
	for i, row := range list {
		if !bytes.Contains(row, []byte("image")) || (drop <= 0 && (maxBase64 == 0 || len(row) <= maxBase64)) {
			continue
		}
		v, ok := decodeRow(row)
		if !ok {
			continue
		}
		changed := rewriteImages(&v, dialect, func(part map[string]any) []any {
			if drop > 0 {
				drop--
				return []any{countNote}
			}
			img, ok := dialect.data(part)
			if maxBase64 == 0 || !ok || len(img.Data) <= maxBase64 {
				return nil
			}
			smaller, fitted, ok := downscaleImage(img, maxBase64)
			if !ok {
				return []any{dialect.Text("[image elided: " + lim.BytesWhy + ", and it could not be made that small]")}
			}
			return []any{dialect.Text(downscaleNote(lim.BytesWhy, fitted)), dialect.withData(part, smaller)}
		})
		if !changed {
			continue
		}
		if out, err := json.Marshal(v); err == nil {
			list[i] = out
		}
	}
	return SliceRows(list, lts)
}

func countImages(list []json.RawMessage, dialect ImageDialect) int {
	n := 0
	for _, row := range list {
		if !bytes.Contains(row, []byte("image")) {
			continue
		}
		v, ok := decodeRow(row)
		if !ok {
			continue
		}
		rewriteImages(&v, dialect, func(map[string]any) []any {
			n++
			return nil
		})
	}
	return n
}

func ImageLimitsOf(e models.Entry, providerName, model string, anySource bool) ImageLimits {
	enforced := func(field string) (string, bool) {
		switch e.Source(field) {
		case models.SourceConfig:
			return fmt.Sprintf("[models.%q] %s in providers/%s.toml", model, field, providerName), true
		case models.SourceMapping:
			return "figaro's catalog mapping for " + providerName, true
		case models.SourceAPI:
			return providerName + "'s model catalog", anySource
		}
		return "", false
	}
	var lim ImageLimits
	if why, ok := enforced("max_prompt_image_size"); ok && e.ImageBytes > 0 {
		lim.MaxBytes = e.ImageBytes
		lim.BytesWhy = fmt.Sprintf("model %s takes images up to %s (%s)", model, tool.FormatSize(e.ImageBytes), why)
	}
	if why, ok := enforced("max_prompt_images"); ok && e.ImageCount > 0 {
		lim.MaxCount = e.ImageCount
		lim.CountWhy = fmt.Sprintf("model %s takes at most %d images per request (%s)", model, e.ImageCount, why)
	}
	return lim
}

type ImageRefused struct {
	Err       error
	MaxBase64 int
}

func (e *ImageRefused) Error() string { return e.Err.Error() }

func (e *ImageRefused) Unwrap() error { return e.Err }
