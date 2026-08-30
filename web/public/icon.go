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

var (
	liteMarkCharcoal = color.NRGBA{R: 0x20, G: 0x2A, B: 0x33, A: 255}
	liteMarkCoral    = color.NRGBA{R: 0xFF, G: 0x6B, B: 0x5E, A: 255}
	liteMarkBlue     = color.NRGBA{R: 0x0E, G: 0x86, B: 0xDD, A: 255}
	liteMarkHalo     = color.NRGBA{R: 0xDD, G: 0xF7, B: 0xE8, A: 255}
	liteMarkGreen    = color.NRGBA{R: 0x22, G: 0xB5, B: 0x73, A: 255}
)

const liteOnlineMarkCanvas = 512

func liteOnlineMarkImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, liteOnlineMarkCanvas, liteOnlineMarkCanvas))
	fillRoundRect(img, 98, 98, 152, 152, 42, liteMarkCharcoal)
	fillRoundRect(img, 98, 262, 152, 152, 42, liteMarkCoral)
	fillRoundRect(img, 262, 262, 152, 152, 42, liteMarkBlue)
	fillCircle(img, 338, 174, 36, liteMarkHalo)
	fillCircle(img, 338, 174, 21, liteMarkGreen)
	return img
}

func defaultPwaIconPNG(size int) []byte {
	var out bytes.Buffer
	if err := png.Encode(&out, flattenIconOntoOpaqueFill(liteOnlineMarkImage(), size, customPwaIconFill)); err != nil {
		return nil
	}
	return out.Bytes()
}

func fillRoundRect(dst *image.NRGBA, x, y, w, h, rx int, c color.NRGBA) {
	if w < 1 || h < 1 {
		return
	}
	if rx < 0 {
		rx = 0
	}
	if rx > w/2 {
		rx = w / 2
	}
	if rx > h/2 {
		rx = h / 2
	}
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			if inRoundRect(px, py, x, y, w, h, rx) {
				dst.SetNRGBA(px, py, c)
			}
		}
	}
}

func inRoundRect(px, py, x, y, w, h, rx int) bool {
	cx := float64(px) + 0.5
	cy := float64(py) + 0.5
	left, top := float64(x), float64(y)
	right, bottom := float64(x+w), float64(y+h)
	if cx < left || cy < top || cx >= right || cy >= bottom {
		return false
	}
	r := float64(rx)
	if rx == 0 || (cx >= left+r && cx < right-r) || (cy >= top+r && cy < bottom-r) {
		return true
	}
	var kx, ky float64
	switch {
	case cx < left+r && cy < top+r:
		kx, ky = left+r, top+r
	case cx >= right-r && cy < top+r:
		kx, ky = right-r, top+r
	case cx < left+r && cy >= bottom-r:
		kx, ky = left+r, bottom-r
	default:
		kx, ky = right-r, bottom-r
	}
	dx, dy := cx-kx, cy-ky
	return dx*dx+dy*dy <= r*r
}

func fillCircle(dst *image.NRGBA, cx, cy, r int, c color.NRGBA) {
	if r < 1 {
		return
	}
	bounds := dst.Bounds()
	for py := cy - r; py <= cy+r; py++ {
		for px := cx - r; px <= cx+r; px++ {
			if !image.Pt(px, py).In(bounds) {
				continue
			}
			dx := float64(px) + 0.5 - float64(cx)
			dy := float64(py) + 0.5 - float64(cy)
			if dx*dx+dy*dy <= float64(r)*float64(r) {
				dst.SetNRGBA(px, py, c)
			}
		}
	}
}
