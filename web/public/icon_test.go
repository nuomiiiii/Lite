package public

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFlattenIconFillsTransparentCorners(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.SetNRGBA(x, y, color.NRGBA{A: 0})
		}
	}
	src.SetNRGBA(1, 1, color.NRGBA{R: 14, G: 134, B: 221, A: 255})
	src.SetNRGBA(2, 1, color.NRGBA{R: 14, G: 134, B: 221, A: 255})
	src.SetNRGBA(1, 2, color.NRGBA{R: 14, G: 134, B: 221, A: 255})
	src.SetNRGBA(2, 2, color.NRGBA{R: 14, G: 134, B: 221, A: 255})

	got := flattenIconOntoOpaqueFill(src, 8, customPwaIconFill)
	r, g, b, a := got.At(0, 0).RGBA()
	if a != 0xffff || r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Fatalf("corner = rgba(%d,%d,%d,%d), want opaque near-white", r>>8, g>>8, b>>8, a>>8)
	}
	cr, cg, cb, ca := got.At(4, 4).RGBA()
	if ca != 0xffff || cr>>8 < 10 || cb>>8 < 180 {
		t.Fatalf("center = rgba(%d,%d,%d,%d), want opaque brand blue", cr>>8, cg>>8, cb>>8, ca>>8)
	}
}

func TestOpaquePwaIconPNGRejectsUndecodableBytes(t *testing.T) {
	if _, ok := opaquePwaIconPNG([]byte("not-an-image"), 180); ok {
		t.Fatal("undecodable bytes were treated as an icon")
	}
}

func TestUploadedLogoStaysOriginalOnFaviconAndFlattensForPWA(t *testing.T) {
	t.Chdir(t.TempDir())
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	if err := os.MkdirAll(DataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := image.NewNRGBA(image.Rect(0, 0, 6, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 6; x++ {
			src.SetNRGBA(x, y, color.NRGBA{A: 0})
		}
	}
	for y := 1; y < 3; y++ {
		for x := 1; x < 5; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 255, G: 107, B: 94, A: 255})
		}
	}
	original := encodePNG(t, src)
	if err := os.WriteFile(filepath.Join(DataDir, FaviconFile), original, 0o644); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	Static(router.Group("/"), router.NoRoute)
	request := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}

	favicon := request("/favicon.ico")
	if favicon.Code != http.StatusOK || favicon.Body.String() != string(original) {
		t.Fatalf("custom favicon.ico was rewritten: status=%d len=%d", favicon.Code, favicon.Body.Len())
	}

	apple := request("/apple-touch-icon.png")
	if apple.Code != http.StatusOK {
		t.Fatalf("GET /apple-touch-icon.png status=%d", apple.Code)
	}
	if !bytes.HasPrefix(apple.Body.Bytes(), []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Fatal("custom apple-touch-icon was not served as PNG")
	}
	if apple.Body.String() == string(original) {
		t.Fatal("transparent custom logo was not flattened for the PWA icon")
	}
	decoded, err := png.Decode(bytes.NewReader(apple.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != defaultPwaIconSize || decoded.Bounds().Dy() != defaultPwaIconSize {
		t.Fatalf("apple-touch size = %v, want %dx%d", decoded.Bounds(), defaultPwaIconSize, defaultPwaIconSize)
	}
	r, g, b, a := decoded.At(0, 0).RGBA()
	if a != 0xffff || r>>8 != 255 || g>>8 != 255 || b>>8 != 255 {
		t.Fatalf("PWA corner = rgba(%d,%d,%d,%d), want opaque white", r>>8, g>>8, b>>8, a>>8)
	}
	cr, _, _, ca := decoded.At(defaultPwaIconSize/2, defaultPwaIconSize/2).RGBA()
	if ca != 0xffff || cr>>8 < 200 {
		t.Fatalf("PWA center = rgba(%d,?,?,%d), want the uploaded coral mark", cr>>8, ca>>8)
	}
}
