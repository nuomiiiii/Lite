package public

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	themePreviewCardMaxEdge  = 720
	themePreviewCardQuality  = 76
	themePreviewCardDirName  = ".komari"
	themePreviewCardFileName = "preview-card.jpg"
	themePreviewMaxPixels    = 40_000_000
)

func isPreviewImagePath(relativePath string) bool {
	switch strings.ToLower(filepath.Ext(relativePath)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func setThemeStaticCacheHeaders(c *gin.Context, requestPath string) {
	switch strings.ToLower(filepath.Ext(requestPath)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".ico":
		c.Header("Cache-Control", "public, max-age=86400")
	case ".js", ".css", ".woff", ".woff2":
		c.Header("Cache-Control", "public, max-age=31536000")
	default:
		c.Header("Cache-Control", "public, max-age=120")
	}
}

func serveThemeFile(c *gin.Context, themeID, relativePath string) {
	if !validThemeID(themeID) {
		c.Status(http.StatusNotFound)
		return
	}
	cleanPath := filepath.Clean(strings.TrimPrefix(relativePath, "/"))
	themeBasePath := filepath.Join(DataDir, ThemesDir, themeID)
	if !isSafePath(themeBasePath, cleanPath) {
		c.Status(http.StatusNotFound)
		return
	}
	localPath := filepath.Join(themeBasePath, cleanPath)
	info, err := os.Stat(localPath)
	if err != nil || info.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}

	servePath := localPath
	if c.Query("card") == "1" && isPreviewImagePath(cleanPath) {
		if cardPath, err := ensureThemePreviewCardFile(themeID, cleanPath, info); err == nil && cardPath != "" {
			servePath = cardPath
		}
	}

	setThemeStaticCacheHeaders(c, servePath)
	c.File(servePath)
}

// EnsureThemePreviewCard generates a card-sized JPEG for an installed theme
// preview so later admin requests can avoid transferring the original screenshot.
func EnsureThemePreviewCard(themeID, previewRelativePath string) error {
	if !validThemeID(themeID) || strings.TrimSpace(previewRelativePath) == "" {
		return nil
	}
	cleanPath := filepath.Clean(strings.TrimPrefix(previewRelativePath, "/"))
	themeBasePath := filepath.Join(DataDir, ThemesDir, themeID)
	if !isSafePath(themeBasePath, cleanPath) || !isPreviewImagePath(cleanPath) {
		return nil
	}
	info, err := os.Stat(filepath.Join(themeBasePath, cleanPath))
	if err != nil {
		return err
	}
	_, err = ensureThemePreviewCardFile(themeID, cleanPath, info)
	return err
}

func ensureThemePreviewCardFile(themeID, cleanPath string, source os.FileInfo) (string, error) {
	themeBasePath := filepath.Join(DataDir, ThemesDir, themeID)
	sourcePath := filepath.Join(themeBasePath, cleanPath)
	cardDir := filepath.Join(themeBasePath, themePreviewCardDirName)
	cardPath := filepath.Join(cardDir, themePreviewCardFileName)
	metaPath := cardPath + ".src"

	expected := sourceFingerprint(cleanPath, source)
	if current, err := os.ReadFile(metaPath); err == nil && string(current) == expected {
		if info, err := os.Stat(cardPath); err == nil && !info.IsDir() {
			return cardPath, nil
		}
	}

	card, err := previewCardJPEGFromFile(sourcePath)
	if err != nil {
		return "", err
	}
	if card == nil {
		return "", nil
	}
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(cardPath, card, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(metaPath, []byte(expected), 0o644); err != nil {
		return "", err
	}
	return cardPath, nil
}

func sourceFingerprint(relativePath string, info os.FileInfo) string {
	return relativePath + "|" + strconv.FormatInt(info.Size(), 10) + "|" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

func previewCardJPEGFromFile(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return PreviewCardJPEG(content)
}

// PreviewCardJPEG returns a resized JPEG suitable for admin theme cards.
// A nil slice means the original is already small enough to serve as-is.
func PreviewCardJPEG(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("empty image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("invalid image size")
	}
	if cfg.Width > themePreviewMaxPixels/cfg.Height {
		return nil, fmt.Errorf("image is too large to preview")
	}

	maxEdge := cfg.Width
	if cfg.Height > maxEdge {
		maxEdge = cfg.Height
	}
	if maxEdge <= themePreviewCardMaxEdge && len(src) <= 80*1024 {
		return nil, nil
	}

	img, err := decodePreviewImage(src)
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid decoded image")
	}
	if width <= themePreviewCardMaxEdge && height <= themePreviewCardMaxEdge && len(src) <= 80*1024 {
		return nil, nil
	}

	scale := float64(themePreviewCardMaxEdge) / float64(width)
	if height > width {
		scale = float64(themePreviewCardMaxEdge) / float64(height)
	}
	if scale > 1 {
		scale = 1
	}
	targetW := int(float64(width)*scale + 0.5)
	targetH := int(float64(height)*scale + 0.5)
	if targetW < 1 {
		targetW = 1
	}
	if targetH < 1 {
		targetH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: themePreviewCardQuality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decodePreviewImage(src []byte) (image.Image, error) {
	reader := bytes.NewReader(src)
	img, format, err := image.Decode(reader)
	if err == nil {
		return img, nil
	}
	if format == "webp" || looksLikeWebP(src) {
		return webp.Decode(bytes.NewReader(src))
	}
	return nil, err
}

func looksLikeWebP(src []byte) bool {
	return len(src) >= 12 && string(src[0:4]) == "RIFF" && string(src[8:12]) == "WEBP"
}
