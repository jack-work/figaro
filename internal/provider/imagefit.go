package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"sort"
	"strings"

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
