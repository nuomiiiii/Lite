package storageupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	appconfig "github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/metric"
	"github.com/nuomiiiii/lite/web/api"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupConfigDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "storage-update.db"))+"?mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open config database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open config SQL database: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	appconfig.SetDb(db)
}

func TestRestrictedControllerRoutes(t *testing.T) {
	setupConfigDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(api.IdentityMiddleware())
	controller := NewController(metric.SQLiteMigrationSummary{Required: true, Layout: "legacy", SourceRows: 10})
	controller.active.Store(true)
	controller.Register(r)

	routes := make(map[string]bool)
	for _, route := range r.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /api/login",
		"GET /api/me",
		"GET /api/oauth",
		"GET /api/oauth_callback",
		"GET " + APIPath + "/auth",
		"GET " + APIPath + "/status",
		"POST " + APIPath + "/retry",
	} {
		if !routes[route] {
			t.Fatalf("required restricted route is missing: %s", route)
		}
	}
	if routes["GET /api/public"] || routes["GET /api/rpc2"] || routes["POST /api/clients/report"] {
		t.Fatalf("ordinary APIs leaked into storage migration routes: %#v", routes)
	}

	request := httptest.NewRequest(http.MethodGet, APIPath+"/status", nil)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unauthenticated status code = %d, want %d", response.Code, http.StatusOK)
	}

	retry := httptest.NewRequest(http.MethodPost, APIPath+"/retry", nil)
	retryResponse := httptest.NewRecorder()
	r.ServeHTTP(retryResponse, retry)
	if retryResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated retry code = %d, want %d", retryResponse.Code, http.StatusUnauthorized)
	}
}

func TestControllerTracksProgressAndCompletion(t *testing.T) {
	controller := NewController(metric.SQLiteMigrationSummary{Required: true, Layout: "normalized", SourceRows: 8})
	controller.migrate = func(_ context.Context, progress metric.MigrationProgressFunc) error {
		progress(metric.MigrationProgress{Phase: metric.MigrationPhaseEncodingPoints, Current: 3, Total: 8, Preserved: 3})
		progress(metric.MigrationProgress{Phase: metric.MigrationPhaseCompleted, Current: 8, Total: 8, Preserved: 8})
		return nil
	}
	controller.run()

	status := controller.snapshot()
	if status.State != "completed" || status.Phase != metric.MigrationPhaseCompleted || status.Progress != 100 {
		t.Fatalf("unexpected completed status: %#v", status)
	}
	if status.Current != 8 || status.Total != 8 || status.Preserved != 8 {
		t.Fatalf("unexpected completed counts: %#v", status)
	}
}

func TestControllerKeepsFailureAvailableForRetry(t *testing.T) {
	controller := NewController(metric.SQLiteMigrationSummary{Required: true})
	want := errors.New("migration failed")
	controller.migrate = func(context.Context, metric.MigrationProgressFunc) error { return want }
	controller.run()

	status := controller.snapshot()
	if status.State != "failed" || status.Error != want.Error() {
		t.Fatalf("unexpected failed status: %#v", status)
	}
	select {
	case <-controller.Done():
		t.Fatal("failed migration closed the restricted listener")
	default:
	}
}

func TestMigrationProgressUsesPhaseRanges(t *testing.T) {
	tests := []struct {
		name     string
		progress metric.MigrationProgress
		want     float64
	}{
		{name: "encoding halfway", progress: metric.MigrationProgress{Phase: metric.MigrationPhaseEncodingPoints, Current: 5, Total: 10}, want: 40},
		{name: "validating halfway", progress: metric.MigrationProgress{Phase: metric.MigrationPhaseValidating, Current: 5, Total: 10}, want: 85},
		{name: "committing halfway", progress: metric.MigrationProgress{Phase: metric.MigrationPhaseCommitting, Current: 1, Total: 2}, want: 92.5},
		{name: "reclaiming complete", progress: metric.MigrationProgress{Phase: metric.MigrationPhaseReclaiming, Current: 1, Total: 1}, want: 99},
		{name: "completed", progress: metric.MigrationProgress{Phase: metric.MigrationPhaseCompleted, Current: 1, Total: 1}, want: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := migrationProgressPercent(test.progress); got != test.want {
				t.Fatalf("migrationProgressPercent(%+v) = %v, want %v", test.progress, got, test.want)
			}
		})
	}
}

func TestControllerProgressDoesNotMoveBackwardAcrossPhases(t *testing.T) {
	controller := NewController(metric.SQLiteMigrationSummary{Required: true})
	for _, progress := range []metric.MigrationProgress{
		{Phase: metric.MigrationPhaseEncodingPoints, Current: 9, Total: 10},
		{Phase: metric.MigrationPhaseEncodingRollups, Current: 1, Total: 10},
		{Phase: metric.MigrationPhaseValidating, Current: 0, Total: 10},
		{Phase: metric.MigrationPhaseCommitting, Current: 0, Total: 1},
		{Phase: metric.MigrationPhaseReclaiming, Current: 0, Total: 1},
	} {
		before := controller.snapshot().Progress
		controller.onProgress(progress)
		if after := controller.snapshot().Progress; after < before {
			t.Fatalf("progress moved backward from %v to %v at phase %q", before, after, progress.Phase)
		}
	}
}
