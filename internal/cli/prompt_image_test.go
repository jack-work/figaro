package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func TestImageFlagsRepeatAndRideTheRequest(t *testing.T) {
	opts, rest, err := extractSendFlags([]string{"--image", "a.jpg", "--image=b.png", "--stdin"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts.images, ",") != "a.jpg,b.png" || !opts.stdin || len(rest) != 0 {
		t.Fatalf("parse: images=%v stdin=%v rest=%v", opts.images, opts.stdin, rest)
	}
	for _, args := range [][]string{{"--image"}, {"--image", "--", "hi"}, {"--image="}} {
		if _, _, err := extractSendFlags(args); err == nil {
			t.Errorf("%v: an --image with no file was taken", args)
		}
	}

	d := dressing{images: []rpc.QuaImage{{MimeType: "image/png", Data: "AAAA"}}}
	if req := d.request("hi"); req.Text != "hi" || len(req.Images) != 1 || req.Images[0].Data != "AAAA" {
		t.Fatalf("the request lost its image: %+v", req)
	}
}

func TestImageIsRefusedWhereNoModelReadsIt(t *testing.T) {
	if _, err := planFork([]string{"abc", "--image", "a.png"}); err == nil {
		t.Error("fork took --image")
	}
	if _, err := planSend([]string{"--image", "a.png", "--", "hi"}); err == nil {
		t.Error("the pager's send took --image")
	}
	for _, o := range []sendOpts{{images: []string{"a"}, exec: true}, {images: []string{"a"}, verbatim: true}} {
		if err := validateSendOpts(o, false); err == nil {
			t.Errorf("--image with %+v", o)
		}
	}
}

func TestReadPromptImages(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "r.png")
	raw := []byte("\x89PNG\r\n\x1a\n rest of a png")
	if err := os.WriteFile(png, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPromptImages([]string{png})
	if err != nil || len(got) != 1 || got[0].MimeType != "image/png" || got[0].Data != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("read: %+v, %v", got, err)
	}
	if got, err := readPromptImages(nil); err != nil || got != nil {
		t.Fatalf("no images must send no images field: %+v, %v", got, err)
	}

	big := filepath.Join(dir, "big.jpg")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(rpc.MaxQuaImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for name, paths := range map[string][]string{
		"oversize":    {big},
		"a directory": {dir},
		"missing":     {filepath.Join(dir, "nope.jpg")},
		"too many":    {png, png, png, png, png},
	} {
		if _, err := readPromptImages(paths); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
}
