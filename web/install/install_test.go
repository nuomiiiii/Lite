package install

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	appconfig "github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/web/backup"
	"github.com/komari-monitor/komari/web/upload"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupInstallRouter(t *testing.T) (*gin.Engine, *gorm.DB, *Controller) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "install.db"))+"?mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open install database: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &appconfig.ConfigItem{}); err != nil {
		t.Fatalf("migrate install database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get install sql database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	appconfig.SetDb(db)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	controller := NewController(db)
	controller.uploadStore = &upload.Store{
		Root:                filepath.Join(t.TempDir(), "uploading"),
		MaxSize:             backup.MaxArchiveSize,
		MaxReservedSize:     backup.MaxArchiveSize,
		MaxSessionsPerOwner: 2,
		SessionTTL:          time.Hour,
		Now:                 time.Now,
	}
	controller.Activate()
	controller.Register(r)
	return r, db, controller
}

func installBackupArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range map[string]string{
		"komari.db":            "sqlite-data",
		"metrics.db":           "metrics-data",
		"komari-backup-markup": "full",
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestInstallChunkedRestoreStagesValidatedBackupOnlyWhileActive(t *testing.T) {
	t.Chdir(t.TempDir())
	oldSchedule, oldExit := scheduleInstallRestart, exitInstallProcess
	scheduleInstallRestart = func(_ time.Duration, task func()) { task() }
	exitInstallProcess = func(int) {}
	t.Cleanup(func() {
		scheduleInstallRestart, exitInstallProcess = oldSchedule, oldExit
	})

	router, _, controller := setupInstallRouter(t)
	archive := installBackupArchive(t)
	initResponse := performJSON(router, http.MethodPost, APIPath+"/upload/init", map[string]any{
		"purpose": "backup", "filename": "upstream-backup.zip", "size": len(archive),
	})
	if initResponse.Code != http.StatusOK {
		t.Fatalf("install upload init = %d: %s", initResponse.Code, initResponse.Body.String())
	}
	var initialized struct {
		Data struct {
			UploadID string `json:"upload_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(initResponse.Body.Bytes(), &initialized); err != nil {
		t.Fatal(err)
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
	chunkRequest := httptest.NewRequest(http.MethodPost, APIPath+"/upload/chunk", &chunkBody)
	chunkRequest.Header.Set("Content-Type", chunkForm.FormDataContentType())
	chunkResponse := httptest.NewRecorder()
	router.ServeHTTP(chunkResponse, chunkRequest)
	if chunkResponse.Code != http.StatusOK {
		t.Fatalf("install upload chunk = %d: %s", chunkResponse.Code, chunkResponse.Body.String())
	}
	mergeResponse := performJSON(router, http.MethodPost, APIPath+"/upload/merge", map[string]string{"upload_id": initialized.Data.UploadID})
	if mergeResponse.Code != http.StatusOK {
		t.Fatalf("install upload merge = %d: %s", mergeResponse.Code, mergeResponse.Body.String())
	}
	if err := backup.ValidateArchive(filepath.Join("data", "backup.zip")); err != nil {
		t.Fatalf("install staged invalid backup: %v", err)
	}

	controller.Deactivate()
	rejected := performJSON(router, http.MethodPost, APIPath+"/upload/init", map[string]any{
		"purpose": "backup", "filename": "backup.zip", "size": len(archive),
	})
	if rejected.Code != http.StatusNotFound {
		t.Fatalf("inactive install upload status = %d, want %d", rejected.Code, http.StatusNotFound)
	}
}

func performJSON(r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	return response
}

func TestInstallRejectsInvalidInputWithoutCreatingAccount(t *testing.T) {
	r, db, _ := setupInstallRouter(t)
	response := performJSON(r, http.MethodPost, APIPath+"/complete", completeRequest{
		Username: "admin", Password: "short", Sitename: "Komari",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid install status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalid install created users: count=%d err=%v", count, err)
	}
}

func TestInstallRejectsWeakPasswordWithoutCreatingAccount(t *testing.T) {
	r, db, _ := setupInstallRouter(t)
	response := performJSON(r, http.MethodPost, APIPath+"/complete", completeRequest{
		Username: "admin", Password: "lowercaseonly1", Sitename: "Komari", MetricDSN: "./data/metrics.db",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("weak password status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("weak password created users: count=%d err=%v", count, err)
	}
}

func TestInstallCompletesAndPersistsSettings(t *testing.T) {
	r, db, _ := setupInstallRouter(t)
	metricDSN := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "metrics.db")) + "?mode=rwc"
	response := performJSON(r, http.MethodPost, APIPath+"/complete", completeRequest{
		Username:    "owner",
		Password:    "Correct-horse-battery-staple1",
		Sitename:    "My Komari",
		Description: "Private monitoring",
		MetricDSN:   metricDSN,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("complete install status = %d: %s", response.Code, response.Body.String())
	}
	var user models.User
	if err := db.First(&user, "username = ?", "owner").Error; err != nil {
		t.Fatalf("find installed admin: %v", err)
	}
	want := map[string]any{
		appconfig.SitenameKey:         "My Komari",
		appconfig.DescriptionKey:      "Private monitoring",
		metricstore.MetricDBDriverKey: "sqlite",
		metricstore.MetricDBDSNKey:    metricDSN,
	}
	got, err := appconfig.GetAll()
	if err != nil {
		t.Fatalf("read all install settings: %v", err)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("setting %s = %#v, want %#v", key, got[key], value)
		}
	}

	repeat := performJSON(r, http.MethodPost, APIPath+"/complete", completeRequest{
		Username: "other", Password: "Another-password1", Sitename: "Other", MetricDSN: "./data/metrics.db",
	})
	if repeat.Code != http.StatusConflict {
		t.Fatalf("repeat install status = %d, want %d", repeat.Code, http.StatusConflict)
	}
}

func TestInstallRejectsUnknownDSN(t *testing.T) {
	r, db, _ := setupInstallRouter(t)
	response := performJSON(r, http.MethodPost, APIPath+"/complete", completeRequest{
		Username: "admin", Password: "Strong-password1", Sitename: "Komari",
		MetricDSN: "not-a-recognized-dsn",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown DSN status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed DSN created users: count=%d err=%v", count, err)
	}
}

func TestCompletedInstallationRoutesCannotRunAgain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCompleted(r)

	statusRequest := httptest.NewRequest(http.MethodGet, APIPath+"/status", nil)
	statusResponse := httptest.NewRecorder()
	r.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("completed status = %d: %s", statusResponse.Code, statusResponse.Body.String())
	}
	var statusPayload struct {
		Status string `json:"status"`
		Data   Status `json:"data"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("decode completed status: %v", err)
	}
	if statusPayload.Status != "success" || statusPayload.Data.State != "completed" || statusPayload.Data.Required {
		t.Fatalf("unexpected completed status: %+v", statusPayload)
	}

	for _, endpoint := range []string{"/complete", "/restore", "/upload/init", "/upload/chunk", "/upload/merge", "/upload/cancel"} {
		request := httptest.NewRequest(http.MethodPost, APIPath+endpoint, nil)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		if response.Code != http.StatusConflict {
			t.Fatalf("repeat request %s status = %d, want %d: %s", endpoint, response.Code, http.StatusConflict, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "installation is already completed") {
			t.Fatalf("repeat request %s returned unexpected body: %s", endpoint, response.Body.String())
		}
	}
}
