package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/public"
)

const (
	marketPreviewMaxSize  = 8 << 20
	marketPreviewCacheDir = "data/cache/theme-previews"
)

func ServeThemeMarketPreview(c *gin.Context) {
	rawURL := strings.TrimSpace(c.Query("url"))
	if err := validateThemeMarketURLSyntax(rawURL); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid preview URL: "+err.Error())
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid preview URL")
		return
	}
	if isPrivateIP(parsed.Hostname()) {
		api.RespondError(c, http.StatusBadRequest, "requests to private or internal addresses are not allowed")
		return
	}

	sum := sha256.Sum256([]byte(rawURL))
	key := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(marketPreviewCacheDir, 0o755); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to cache theme preview: "+err.Error())
		return
	}
	originalPath := filepath.Join(marketPreviewCacheDir, key+".bin")
	typePath := filepath.Join(marketPreviewCacheDir, key+".type")
	cardPath := filepath.Join(marketPreviewCacheDir, key+".card.jpg")

	contentType, err := ensureMarketPreviewOriginal(rawURL, originalPath, typePath)
	if err != nil {
		api.RespondError(c, http.StatusBadGateway, "Failed to load theme preview: "+err.Error())
		return
	}

	servePath := originalPath
	serveType := contentType
	if c.Query("card") == "1" {
		if cached, err := os.ReadFile(cardPath); err == nil && len(cached) > 0 {
			servePath = cardPath
			serveType = "image/jpeg"
		} else if card, err := ensureMarketPreviewCard(originalPath); err == nil && len(card) > 0 {
			if err := os.WriteFile(cardPath, card, 0o644); err == nil {
				servePath = cardPath
				serveType = "image/jpeg"
			}
		}
	}

	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Content-Type", serveType)
	c.File(servePath)
}

func ensureMarketPreviewOriginal(rawURL, originalPath, typePath string) (string, error) {
	if contentType, err := os.ReadFile(typePath); err == nil {
		if info, err := os.Stat(originalPath); err == nil && !info.IsDir() && info.Size() > 0 {
			return strings.TrimSpace(string(contentType)), nil
		}
	}

	data, err := downloadThemeMarketURL(rawURL, marketPreviewMaxSize)
	if err != nil {
		return "", err
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		return "", errors.New("preview URL did not return an image")
	}
	if err := os.WriteFile(originalPath, data, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(typePath, []byte(contentType), 0o644); err != nil {
		return "", err
	}
	return contentType, nil
}

func ensureMarketPreviewCard(originalPath string) ([]byte, error) {
	data, err := os.ReadFile(originalPath)
	if err != nil {
		return nil, err
	}
	return public.PreviewCardJPEG(data)
}
