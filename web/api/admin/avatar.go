package admin

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "image/jpeg"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/web/api"
)

const (
	avatarOutputSize     = 256
	avatarMaxUploadBytes = 1 << 20
	avatarMaxPixels      = 16_000_000
	avatarMaxPNGBytes    = 512 << 10
)

var avatarVersionPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func avatarDir(userUUID string) string {
	return filepath.Join(".", "data", "avatars", userUUID)
}

func avatarPath(userUUID, version string) string {
	return filepath.Join(avatarDir(userUUID), version+".png")
}

func avatarURL(version string) string {
	if strings.TrimSpace(version) == "" {
		return ""
	}
	return "/api/admin/account/avatar/" + version
}

func UploadAccountAvatar(c *gin.Context) {
	userUUID, _ := c.Get("uuid")
	uuidStr, _ := userUUID.(string)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, avatarMaxUploadBytes)
	file, err := c.FormFile("file")
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Avatar file is required")
		return
	}
	src, err := file.Open()
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Failed to read avatar")
		return
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Failed to read avatar")
		return
	}
	pngBytes, err := normalizeAvatarPNG(data)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	user, err := accounts.GetUserByUUID(uuidStr)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to load account")
		return
	}
	version := strings.ReplaceAll(uuid.NewString(), "-", "")
	dir := avatarDir(uuidStr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to store avatar")
		return
	}
	tmpPath := filepath.Join(dir, version+".png.tmp")
	finalPath := avatarPath(uuidStr, version)
	if err := os.WriteFile(tmpPath, pngBytes, 0o644); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to store avatar")
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		api.RespondError(c, http.StatusInternalServerError, "Failed to store avatar")
		return
	}
	previous := user.AvatarVersion
	if err := dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", uuidStr).Updates(map[string]any{
		"avatar_version": version,
		"updated_at":     time.Now().UTC(),
	}).Error; err != nil {
		_ = os.Remove(finalPath)
		api.RespondError(c, http.StatusInternalServerError, "Failed to save avatar")
		return
	}
	if previous != "" && previous != version {
		_ = os.Remove(avatarPath(uuidStr, previous))
	}
	auditlog.Event(c.ClientIP(), uuidStr, "info", "audit.avatar_update", nil)
	api.RespondSuccess(c, gin.H{"avatar_url": avatarURL(version)})
}

func GetAccountAvatar(c *gin.Context) {
	userUUID, _ := c.Get("uuid")
	uuidStr, _ := userUUID.(string)
	version := c.Param("version")
	if uuidStr == "" || !avatarVersionPattern.MatchString(version) {
		c.Status(http.StatusNotFound)
		return
	}
	user, err := accounts.GetUserByUUID(uuidStr)
	if err != nil || user.AvatarVersion != version {
		c.Status(http.StatusNotFound)
		return
	}
	path := avatarPath(uuidStr, version)
	if _, err := os.Stat(path); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", "image/png")
	c.Header("Cache-Control", "private, max-age=3600")
	c.File(path)
}

func DeleteAccountAvatar(c *gin.Context) {
	userUUID, _ := c.Get("uuid")
	uuidStr, _ := userUUID.(string)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	user, err := accounts.GetUserByUUID(uuidStr)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to load account")
		return
	}
	if err := dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", uuidStr).Updates(map[string]any{
		"avatar_version": "",
		"updated_at":     time.Now().UTC(),
	}).Error; err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to reset avatar")
		return
	}
	if user.AvatarVersion != "" {
		_ = os.Remove(avatarPath(uuidStr, user.AvatarVersion))
	}
	auditlog.Event(c.ClientIP(), uuidStr, "info", "audit.avatar_remove", nil)
	api.RespondSuccess(c, gin.H{"avatar_url": ""})
}

func normalizeAvatarPNG(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errInvalidAvatar
	}
	bounds := img.Bounds()
	if bounds.Dx()*bounds.Dy() > avatarMaxPixels || bounds.Dx() < 1 || bounds.Dy() < 1 {
		return nil, errInvalidAvatar
	}
	dst := scaleAvatar(img, avatarOutputSize)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, errInvalidAvatar
	}
	if buf.Len() > avatarMaxPNGBytes {
		return nil, errInvalidAvatar
	}
	return buf.Bytes(), nil
}

func scaleAvatar(src image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	bounds := src.Bounds()
	for y := 0; y < size; y++ {
		sy := bounds.Min.Y + y*bounds.Dy()/size
		for x := 0; x < size; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/size
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

var errInvalidAvatar = imageError("invalid avatar image")

type imageError string

func (e imageError) Error() string { return string(e) }
