// fitcmd is a hand inspector for the image fitter: it prints what FitImage did
// to a file and writes the fitted bytes out so a human can LOOK at them.
//
// It exists because the unit tests can only assert that a picture fits and
// still decodes — they cannot tell you whether it is still legible, which is
// the only property that actually matters to a model reading a screenshot.
//
//	go run ./internal/tool/testdata/fitcmd <in> <out> [maxBase64Bytes]
package main

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/tool"
)

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	lim := tool.DefaultImageLimits()
	if len(os.Args) > 3 {
		var n int
		fmt.Sscanf(os.Args[3], "%d", &n)
		lim.MaxBase64 = n
	}
	f, err := tool.FitImage(raw, "image/png", lim)
	if err != nil {
		fmt.Println("ERR:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %dx%d -> %dx%d %s  b64=%d (%s) resized=%v recoded=%v\n  note=%s\n",
		os.Args[1], f.OrigW, f.OrigH, f.W, f.H, f.MimeType, len(f.Data), tool.FormatSize(len(f.Data)), f.Resized, f.Recoded, f.Note())
	out, _ := base64.StdEncoding.DecodeString(f.Data)
	os.WriteFile(os.Args[2], out, 0o644)
}
