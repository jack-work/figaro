// mkfixtures writes the image fixtures the end-to-end check reads.
//
// The point of the fixtures is FALSIFIABILITY: each carries a random code in
// letters big enough to survive a downscale, so a model that can genuinely see
// the picture reports the code and a model that cannot has nothing to guess.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func label(text string, scale int) *image.Alpha {
	w := (len(text) + 2) * 7
	h := 15
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	d := &font.Drawer{
		Dst:  mask,
		Src:  image.NewUniform(color.Alpha{A: 255}),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(7, 11),
	}
	d.DrawString(text)

	out := image.NewAlpha(image.Rect(0, 0, w*scale, h*scale))
	for y := range h * scale {
		for x := range w * scale {
			out.SetAlpha(x, y, mask.AlphaAt(x/scale, y/scale))
		}
	}
	return out
}

func fixture(w, h int, text string, scale int, seed int64) *image.RGBA {
	rng := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Dithered gradient: PNG cannot collapse it, so the file is honestly large.
	for y := range h {
		for x := range w {
			base := x*120/w + y*60/h + 20
			d := rng.Intn(15) - 7
			img.Set(x, y, color.RGBA{
				R: clamp(base + d), G: clamp(base/2 + d), B: clamp(base + 40 - d), A: 255,
			})
		}
	}
	m := label(text, scale)
	mb := m.Bounds()
	at := image.Rect((w-mb.Dx())/2, (h-mb.Dy())/2, (w-mb.Dx())/2+mb.Dx(), (h-mb.Dy())/2+mb.Dy())
	draw.DrawMask(img, at, image.NewUniform(color.RGBA{R: 255, G: 240, B: 90, A: 255}),
		image.Point{}, m, mb.Min, draw.Over)
	return img
}

func clamp(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func writeJPEG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 92}); err != nil {
		panic(err)
	}
}

func main() {
	dir := os.Args[1]
	codes := map[string]string{}
	// An alphabet with NO confusable pairs.
	//
	// 5/S, 8/B, 0/O, 1/I/L, 2/Z, 6/G and P/F are each one JPEG artifact away
	// from one another in an upscaled bitmap font. A model that answers 9X584
	// for 9XS84 has plainly SEEN the picture, so a code containing both glyphs
	// measures its OCR rather than whether the image arrived — and the arrival
	// is the only thing under test. (Measured: GPT-5.6 did exactly that and
	// turned a working fix into a red run.) Sixteen symbols still leave
	// 1,048,576 codes, so nothing here is guessable blind.
	alphabet := []rune("ACEFHJKMRTWXY349")
	rng := rand.New(rand.NewSource(int64(os.Getpid())))
	code := func() string {
		s := make([]rune, 5)
		for i := range s {
			s[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(s)
	}

	for _, spec := range []struct {
		name       string
		w, h       int
		scale      int
		jpegOutput bool
	}{
		{"huge.png", 3840, 2160, 22, false},
		{"medium.png", 1600, 900, 10, false},
		{"small.png", 320, 200, 2, false},
		{"photo.jpg", 2400, 1600, 20, true},
	} {
		c := code()
		codes[spec.name] = c
		img := fixture(spec.w, spec.h, "CODE "+c, spec.scale, int64(len(spec.name)))
		path := filepath.Join(dir, spec.name)
		if spec.jpegOutput {
			writeJPEG(path, img)
		} else {
			writePNG(path, img)
		}
		st, _ := os.Stat(path)
		fmt.Printf("%-12s %5dx%-5d %9d bytes  code=%s\n", spec.name, spec.w, spec.h, st.Size(), c)
	}
}
