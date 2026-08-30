package public

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"golang.org/x/image/draw"
)

const (
	defaultPwaIconSize = 180
	pwaIcon192Size     = 192
	pwaIcon512Size     = 512
	pwaIconMaxPixels   = 16_000_000
)

var customPwaIconFill = color.NRGBA{R: 255, G: 255, B: 255, A: 255}

const applicationIconHTML = `<link rel="icon" type="image/png" href="/favicon.png" /><link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png" />`

func opaquePwaIconPNG(data []byte, size int) ([]byte, bool) {
	if size < 1 || len(data) == 0 {
		return nil, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return nil, false
	}
	if cfg.Height > 0 && cfg.Width > pwaIconMaxPixels/cfg.Height {
		return nil, false
	}
	src, err := decodePreviewImage(data)
	if err != nil {
		return nil, false
	}
	var out bytes.Buffer
	if err := png.Encode(&out, flattenIconOntoOpaqueFill(src, size, customPwaIconFill)); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

func flattenIconOntoOpaqueFill(src image.Image, size int, fill color.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			out.SetNRGBA(x, y, fill)
		}
	}
	if src == nil {
		return out
	}
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW < 1 || srcH < 1 {
		return out
	}

	scale := float64(size) / float64(srcW)
	if float64(srcH)*scale > float64(size) {
		scale = float64(size) / float64(srcH)
	}
	destW := int(float64(srcW)*scale + 0.5)
	destH := int(float64(srcH)*scale + 0.5)
	if destW < 1 {
		destW = 1
	}
	if destH < 1 {
		destH = 1
	}
	left := (size - destW) / 2
	top := (size - destH) / 2
	draw.CatmullRom.Scale(out, image.Rect(left, top, left+destW, top+destH), src, bounds, draw.Over, nil)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px := out.NRGBAAt(x, y)
			px.A = 255
			out.SetNRGBA(x, y, px)
		}
	}
	return out
}
