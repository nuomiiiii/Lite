package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/utils/httpsserver"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/backup"
	"github.com/nuomiiiii/lite/web/upload"
)

func archiveUploadStore(t *testing.T) *upload.Store {
	t.Helper()
	return &upload.Store{
		Root:                filepath.Join(t.TempDir(), "uploading"),
		MaxSize:             backup.MaxArchiveSize,
		MaxReservedSize:     backup.MaxArchiveSize,
		MaxSessionsPerOwner: 2,
		SessionTTL:          time.Hour,
		Now:                 time.Now,
	}
}

func archiveUploadRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(ctx *gin.Context) {
		api.SetPrincipal(ctx, rpc.NewUserPrincipal("test-admin"))
		ctx.Next()
	})
	handler := newArchiveUploadHandler(archiveUploadStore(t))
	group := router.Group("/api/admin/upload")
	group.POST("/init", handler.Init)
	group.POST("/chunk", handler.Chunk)
	group.POST("/merge", handler.Merge)
	group.POST("/cancel", handler.Cancel)
	return router
}

func postJSON(t *testing.T, router http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func chunkUploadRequest(t *testing.T, path, uploadID string, index int, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("upload_id", uploadID); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("chunk_index", fmt.Sprint(index)); err != nil {
		t.Fatal(err)
	}
	chunk, err := form.CreateFormFile("chunk_data", "chunk.part")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chunk.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	return request
}

func uploadArchiveThroughHandler(t *testing.T, router http.Handler, purpose upload.Purpose, filename string, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	initResponse := postJSON(t, router, "/api/admin/upload/init", map[string]any{
		"purpose": purpose, "filename": filename, "size": len(archive),
	})
	if initResponse.Code != http.StatusOK {
		t.Fatalf("init upload status = %d: %s", initResponse.Code, initResponse.Body.String())
	}
	var initialized struct {
		Data struct {
			UploadID string `json:"upload_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(initResponse.Body.Bytes(), &initialized); err != nil {
		t.Fatal(err)
	}
	chunkRequest := chunkUploadRequest(t, "/api/admin/upload/chunk", initialized.Data.UploadID, 0, archive)
	chunkResponse := httptest.NewRecorder()
	router.ServeHTTP(chunkResponse, chunkRequest)
	if chunkResponse.Code != http.StatusOK {
		t.Fatalf("chunk upload status = %d: %s", chunkResponse.Code, chunkResponse.Body.String())
	}
	return postJSON(t, router, "/api/admin/upload/merge", map[string]string{"upload_id": initialized.Data.UploadID})
}

func runRestartImmediately(t *testing.T) {
	t.Helper()
	oldSchedule, oldExit := scheduleAdminRestart, exitAdminProcess
	scheduleAdminRestart = func(_ time.Duration, task func()) { task() }
	exitAdminProcess = func(int) {}
	t.Cleanup(func() {
		scheduleAdminRestart, exitAdminProcess = oldSchedule, oldExit
	})
}

func TestChunkedBackupUploadStagesOnlyValidatedArchive(t *testing.T) {
	t.Chdir(t.TempDir())
	runRestartImmediately(t)
	router := archiveUploadRouter(t)
	archive := themeArchive(t, map[string]string{
		"lite.db":              "sqlite-data",
		"metrics.db":           "metrics-data",
		"komari-backup-markup": "full",
	})
	response := uploadArchiveThroughHandler(t, router, upload.PurposeBackup, "backup.zip", archive)
	if response.Code != http.StatusOK {
		t.Fatalf("merge backup status = %d: %s", response.Code, response.Body.String())
	}
	staged := filepath.Join("data", "backup.zip")
	if err := backup.ValidateArchive(staged); err != nil {
		t.Fatalf("staged backup is invalid: %v", err)
	}
}

func TestUploadInitAcceptsUpstreamOptionalFilename(t *testing.T) {
	t.Chdir(t.TempDir())
	response := postJSON(t, archiveUploadRouter(t), "/api/admin/upload/init", map[string]any{
		"purpose": upload.PurposeTheme,
		"size":    3,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("upstream-compatible init status = %d: %s", response.Code, response.Body.String())
	}
}

func TestChunkedBackupUploadFailurePreservesExistingStagedBackup(t *testing.T) {
	for name, archive := range map[string][]byte{
		"not zip":        []byte("not a zip"),
		"missing marker": themeArchive(t, map[string]string{"lite.db": "sqlite-data"}),
		"missing database": themeArchive(t, map[string]string{
			"komari-backup-markup": "config",
		}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			runRestartImmediately(t)
			if err := os.MkdirAll("data", 0o755); err != nil {
				t.Fatal(err)
			}
			const oldBackup = "existing staged backup"
			if err := os.WriteFile(filepath.Join("data", "backup.zip"), []byte(oldBackup), 0o600); err != nil {
				t.Fatal(err)
			}
			response := uploadArchiveThroughHandler(t, archiveUploadRouter(t), upload.PurposeBackup, "backup.zip", archive)
			if response.Code == http.StatusOK {
				t.Fatalf("invalid backup was accepted: %s", response.Body.String())
			}
			content, err := os.ReadFile(filepath.Join("data", "backup.zip"))
			if err != nil || string(content) != oldBackup {
				t.Fatalf("existing staged backup changed: content=%q err=%v", content, err)
			}
		})
	}
}

func TestChunkedThemeUploadInstallsAndFailedUpdatePreservesTheme(t *testing.T) {
	t.Chdir(t.TempDir())
	router := archiveUploadRouter(t)
	valid := themeArchive(t, map[string]string{
		"komari-theme.json": `{"name":"Uploaded","short":"uploaded","configuration":{"type":"managed","data":[]}}`,
		"dist/index.html":   "new theme",
	})
	response := uploadArchiveThroughHandler(t, router, upload.PurposeTheme, "uploaded.zip", valid)
	if response.Code != http.StatusOK {
		t.Fatalf("merge theme status = %d: %s", response.Code, response.Body.String())
	}
	indexPath := filepath.Join("data", "theme", "uploaded", "dist", "index.html")
	if content, err := os.ReadFile(indexPath); err != nil || string(content) != "new theme" {
		t.Fatalf("installed theme content=%q err=%v", content, err)
	}

	invalid := themeArchive(t, map[string]string{
		"komari-theme.json": `{"name":"Uploaded","short":"uploaded","configuration":{"type":"managed","data":[]}}`,
		"dist/app.js":       "missing index",
	})
	response = uploadArchiveThroughHandler(t, router, upload.PurposeTheme, "uploaded.zip", invalid)
	if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "dist/index.html") {
		t.Fatalf("invalid theme response = %d: %s", response.Code, response.Body.String())
	}
	if content, err := os.ReadFile(indexPath); err != nil || string(content) != "new theme" {
		t.Fatalf("existing theme changed after failed upload: content=%q err=%v", content, err)
	}
}

func writeArchiveUploadCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "monitor.example"},
		DNSNames:     []string{"monitor.example"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestChunkedThemeUploadThroughBuiltInHTTPSRedirect(t *testing.T) {
	t.Chdir(t.TempDir())
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(ctx *gin.Context) {
		cookie, err := ctx.Cookie("session_token")
		if err != nil || cookie != "built-in-https-session" {
			ctx.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		api.SetPrincipal(ctx, rpc.NewUserPrincipal("https-admin"))
		ctx.Next()
	})
	handler := newArchiveUploadHandler(archiveUploadStore(t))
	group := router.Group("/api/admin/upload")
	group.POST("/init", handler.Init)
	group.POST("/chunk", handler.Chunk)
	group.POST("/merge", handler.Merge)

	certPath, keyPath := writeArchiveUploadCertificate(t)
	manager := httpsserver.NewManager()
	settings := httpsserver.Settings{
		Enabled: true, Listen: "127.0.0.1:0", RedirectHTTP: true,
		CertificatePath: certPath, PrivateKeyPath: keyPath,
	}
	if err := manager.Start(router, settings); err != nil {
		t.Fatalf("start built-in HTTPS: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	plainServer := httptest.NewServer(manager.HTTPRedirectHandler(router))
	defer plainServer.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	plainURL, err := url.Parse(plainServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(plainURL, []*http.Cookie{{Name: "session_token", Value: "built-in-https-session", Path: "/"}})
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Test-only certificate for monitor.example is intentionally reached through 127.0.0.1.
		}},
	}
	archive := themeArchive(t, map[string]string{
		"komari-theme.json": `{"name":"HTTPS","short":"https-theme","configuration":{"type":"managed","data":[]}}`,
		"dist/index.html":   "installed through built-in HTTPS",
	})
	do := func(path, contentType string, body []byte) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, plainServer.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", contentType)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("request through built-in HTTPS: %v", err)
		}
		if response.Request.URL.Scheme != "https" {
			response.Body.Close()
			t.Fatalf("request did not finish on built-in HTTPS: %s", response.Request.URL)
		}
		return response
	}

	initBody, _ := json.Marshal(map[string]any{"purpose": "theme", "filename": "https-theme.zip", "size": len(archive)})
	response := do("/api/admin/upload/init", "application/json", initBody)
	var initialized struct {
		Status string `json:"status"`
		Data   struct {
			UploadID string `json:"upload_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&initialized); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || initialized.Status != "success" || initialized.Data.UploadID == "" {
		t.Fatalf("HTTPS upload init status=%d payload=%+v", response.StatusCode, initialized)
	}

	var chunkBody bytes.Buffer
	chunkForm := multipart.NewWriter(&chunkBody)
	if err := chunkForm.WriteField("upload_id", initialized.Data.UploadID); err != nil {
		t.Fatal(err)
	}
	if err := chunkForm.WriteField("chunk_index", "0"); err != nil {
		t.Fatal(err)
	}
	chunkPart, err := chunkForm.CreateFormFile("chunk_data", "chunk.part")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chunkPart.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := chunkForm.Close(); err != nil {
		t.Fatal(err)
	}
	response = do("/api/admin/upload/chunk", chunkForm.FormDataContentType(), chunkBody.Bytes())
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS upload chunk status=%d", response.StatusCode)
	}
	mergeBody, _ := json.Marshal(map[string]string{"upload_id": initialized.Data.UploadID})
	response = do("/api/admin/upload/merge", "application/json", mergeBody)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS upload merge status=%d", response.StatusCode)
	}
	content, err := os.ReadFile(filepath.Join("data", "theme", "https-theme", "dist", "index.html"))
	if err != nil || string(content) != "installed through built-in HTTPS" {
		t.Fatalf("theme installed through HTTPS content=%q err=%v", content, err)
	}
}
