package public

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewCardJPEGShrinksLargeImages(t *testing.T) {
	src := makeOpaquePNG(t, 1600, 900)
	card, err := PreviewCardJPEG(src)
	if err != nil {
		t.Fatalf("PreviewCardJPEG() error = %v", err)
	}
	if len(card) == 0 {
		t.Fatal("PreviewCardJPEG() returned empty card")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(card))
	if err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if cfg.Width > themePreviewCardMaxEdge || cfg.Height > themePreviewCardMaxEdge {
		t.Fatalf("card size %dx%d exceeds %d", cfg.Width, cfg.Height, themePreviewCardMaxEdge)
	}
}

func TestEnsureThemePreviewCardWritesSidecar(t *testing.T) {
	t.Chdir(t.TempDir())
	themeDir := filepath.Join(DataDir, ThemesDir, "glass")
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	previewPath := filepath.Join(themeDir, "preview.png")
	if err := os.WriteFile(previewPath, makeOpaquePNG(t, 1280, 720), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureThemePreviewCard("glass", "preview.png"); err != nil {
		t.Fatalf("EnsureThemePreviewCard() error = %v", err)
	}
	cardPath := filepath.Join(themeDir, themePreviewCardDirName, themePreviewCardFileName)
	info, err := os.Stat(cardPath)
	if err != nil {
		t.Fatalf("card sidecar missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("card sidecar is empty")
	}
}

func makeOpaquePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
