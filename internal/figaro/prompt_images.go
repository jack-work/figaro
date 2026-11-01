package figaro

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/tool"
)

var errImageWithoutText = errors.New("an image is sent with a prompt: give text too")

var promptImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

func promptImages(in []rpc.QuaImage, text string, budget int) ([]message.Content, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(text) == "" {
		return nil, errImageWithoutText
	}
	if len(in) > rpc.MaxQuaImages {
		return nil, fmt.Errorf("%d images: a prompt carries at most %d", len(in), rpc.MaxQuaImages)
	}
	per := min(budget/len(in), tool.ProviderImageCeiling)
	out := make([]message.Content, 0, len(in))
	for i, img := range in {
		n := i + 1
		if len(img.Data) > base64.StdEncoding.EncodedLen(rpc.MaxQuaImageBytes) {
			return nil, fmt.Errorf("image %d is over the %s limit", n, tool.FormatSize(rpc.MaxQuaImageBytes))
		}
		raw, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			return nil, fmt.Errorf("image %d is not base64: %w", n, err)
		}
		mime := http.DetectContentType(raw)
		if !promptImageTypes[mime] {
			return nil, fmt.Errorf("image %d is %s, not png, jpeg, gif or webp", n, mime)
		}
		fitted, err := tool.FitImage(raw, mime, tool.ImageLimits{MaxBase64: per})
		if err != nil {
			return nil, fmt.Errorf("image %d: %w", n, err)
		}
		out = append(out, message.ImageContent(fitted.MimeType, fitted.Data))
	}
	return out, nil
}

func fitPromptImagesTotal(images []message.Content, budget int) []message.Content {
	total := 0
	for _, c := range images {
		total += len(c.Data)
	}
	if total <= budget {
		return images
	}
	per := min(budget/len(images), tool.ProviderImageCeiling)
	out := make([]message.Content, 0, len(images))
	for _, c := range images {
		if len(c.Data) <= per {
			out = append(out, c)
			continue
		}
		if fitted, ok := refitPromptImage(c, per); ok {
			out = append(out, fitted)
			continue
		}
		out = append(out, message.TextContent(fmt.Sprintf(
			"[image omitted: it could not be shrunk to share this message's %s image budget with %d others]",
			tool.FormatSize(budget), len(images)-1)))
	}
	return out
}

func refitPromptImage(c message.Content, limit int) (message.Content, bool) {
	raw, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil {
		return message.Content{}, false
	}
	fitted, err := tool.FitImage(raw, c.MimeType, tool.ImageLimits{MaxBase64: limit})
	if err != nil {
		return message.Content{}, false
	}
	return message.ImageContent(fitted.MimeType, fitted.Data), true
}
