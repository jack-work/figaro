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
