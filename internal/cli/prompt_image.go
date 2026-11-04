package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/jack-work/figaro/api/rpc"
)

var errImageNeedsPrompt = errors.New("--image goes with a prompt: give one after -- or with --stdin")

func readPromptImages(paths []string) ([]rpc.QuaImage, error) {
	if len(paths) > rpc.MaxQuaImages {
		return nil, fmt.Errorf("--image given %d times: a prompt carries at most %d", len(paths), rpc.MaxQuaImages)
	}
	if len(paths) > 0 {
		if err := needCapability(rpc.CapQuaImages, "images on a prompt"); err != nil {
			return nil, err
		}
	}
	out := make([]rpc.QuaImage, 0, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("--image: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("--image %s: not a regular file", p)
		}
		if info.Size() > rpc.MaxQuaImageBytes {
			return nil, fmt.Errorf("--image %s: %d bytes is over the %d MB limit", p, info.Size(), rpc.MaxQuaImageBytes>>20)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("--image: %w", err)
		}
		out = append(out, rpc.QuaImage{
			MimeType: http.DetectContentType(raw),
			Data:     base64.StdEncoding.EncodeToString(raw),
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
