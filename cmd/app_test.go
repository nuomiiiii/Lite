package cmd

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	installweb "github.com/nuomiiiii/lite/web/install"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCommitRestoreClearsSessionsBeforeCommit(t *testing.T) {
	order := make([]string, 0, 3)
	previousChecker := hasPendingRestoreFn
	previousInvalidate := invalidateRestoredSessions
	previousCommit := commitPendingRestoreFn
	previousRevoke := revokeRemoteAfterRestore
	t.Cleanup(func() {
		hasPendingRestoreFn = previousChecker
		invalidateRestoredSessions = previousInvalidate
		commitPendingRestoreFn = previousCommit
		revokeRemoteAfterRestore = previousRevoke
	})
	hasPendingRestoreFn = func() bool { return true }
	invalidateRestoredSessions = func() error {
		order = append(order, "sessions")
		return nil
	}
	commitPendingRestoreFn = func() error {
		order = append(order, "commit")
		return nil
	}
	revokeRemoteAfterRestore = func() { order = append(order, "revoke") }
	if err := (&App{}).CommitRestore(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "sessions,commit,revoke" {
		t.Fatalf("restore commit order = %q", got)
	}
}

func TestCommitRestoreDoesNotCommitWhenSessionInvalidateFails(t *testing.T) {
	committed := false
	revoked := false
	previousChecker := hasPendingRestoreFn
	previousInvalidate := invalidateRestoredSessions
	previousCommit := commitPendingRestoreFn
	previousRevoke := revokeRemoteAfterRestore
	t.Cleanup(func() {
		hasPendingRestoreFn = previousChecker
		invalidateRestoredSessions = previousInvalidate
		commitPendingRestoreFn = previousCommit
		revokeRemoteAfterRestore = previousRevoke
	})
	hasPendingRestoreFn = func() bool { return true }
	invalidateRestoredSessions = func() error { return errors.New("session delete failed") }
	commitPendingRestoreFn = func() error {
		committed = true
		return nil
	}
	revokeRemoteAfterRestore = func() { revoked = true }
	err := (&App{}).CommitRestore()
	if err == nil {
		t.Fatal("expected session cleanup failure to abort restore commit")
	}
	if committed {
		t.Fatal("CommitPendingRestore ran after session cleanup failed")
	}
	if revoked {
		t.Fatal("remote revoke ran after session cleanup failed")
	}
}

func TestCommitRestoreSkipsSessionCleanupWhenNothingPending(t *testing.T) {
	invalidated := false
	previousChecker := hasPendingRestoreFn
	previousInvalidate := invalidateRestoredSessions
	previousCommit := commitPendingRestoreFn
	previousRevoke := revokeRemoteAfterRestore
	t.Cleanup(func() {
		hasPendingRestoreFn = previousChecker
		invalidateRestoredSessions = previousInvalidate
		commitPendingRestoreFn = previousCommit
		revokeRemoteAfterRestore = previousRevoke
	})
	hasPendingRestoreFn = func() bool { return false }
	invalidateRestoredSessions = func() error {
		invalidated = true
		return nil
	}
	commitPendingRestoreFn = func() error {
		t.Fatal("commit should not run without a pending restore")
		return nil
	}
	revokeRemoteAfterRestore = func() { t.Fatal("remote revoke should not run without a pending restore") }
	if err := (&App{}).CommitRestore(); err != nil {
		t.Fatal(err)
	}
	if invalidated {
		t.Fatal("normal sessions were deleted without a pending restore")
	}
}

func TestListenAndFinalizeStartupDoesNotCommitWhenPortIsOccupied(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve occupied port: %v", err)
	}
	defer occupied.Close()

	commitCalled := false
	backgroundCalled := false
	listener, err := listenAndFinalizeStartup(
		occupied.Addr().String(),
		func() error { commitCalled = true; return nil },
		func() error { backgroundCalled = true; return nil },
	)
	if err == nil {
		if listener != nil {
			listener.Close()
		}
		t.Fatal("expected occupied listener to fail")
	}
	if commitCalled || backgroundCalled {
		t.Fatalf("startup finalized after bind failure: commit=%t background=%t", commitCalled, backgroundCalled)
	}
}

func TestListenAndFinalizeStartupCommitsOnlyAfterBinding(t *testing.T) {
	order := make([]string, 0, 2)
	listener, err := listenAndFinalizeStartup(
		"127.0.0.1:0",
		func() error { order = append(order, "commit"); return nil },
		func() error { order = append(order, "background"); return nil },
	)
	if err != nil {
		t.Fatalf("listen and finalize startup: %v", err)
	}
	defer listener.Close()
	if got := strings.Join(order, ","); got != "commit,background" {
		t.Fatalf("startup order = %q, want commit,background", got)
	}
}

func TestFirstRunInstallRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(firstRunInstallRedirect())
	router.Any("/*path", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	tests := []struct {
		name         string
		method       string
		path         string
		wantStatus   int
		wantLocation string
	}{
		{name: "root", method: http.MethodGet, path: "/", wantStatus: http.StatusTemporaryRedirect, wantLocation: installweb.PagePath},
		{name: "public route", method: http.MethodGet, path: "/instance/example", wantStatus: http.StatusTemporaryRedirect, wantLocation: installweb.PagePath},
		{name: "admin route", method: http.MethodGet, path: "/admin", wantStatus: http.StatusTemporaryRedirect, wantLocation: installweb.PagePath},
		{name: "install page", method: http.MethodGet, path: installweb.PagePath, wantStatus: http.StatusNoContent},
		{name: "install API", method: http.MethodGet, path: installweb.APIPath + "/status", wantStatus: http.StatusNoContent},
		{name: "system asset", method: http.MethodGet, path: "/system-assets/assets/entry.js", wantStatus: http.StatusNoContent},
		{name: "favicon", method: http.MethodGet, path: "/favicon.ico", wantStatus: http.StatusNoContent},
		{name: "non GET request", method: http.MethodPost, path: "/", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if location := recorder.Header().Get("Location"); location != tt.wantLocation {
				t.Fatalf("Location = %q, want %q", location, tt.wantLocation)
			}
		})
	}
}

func TestMigrateAllowRemoteManagementUpgradeKeepsEnabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:migrate-remote-upgrade?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open config database: %v", err)
	}
	config.SetDb(db)
	if err := config.Set(config.SitenameKey, "Existing Lite"); err != nil {
		t.Fatalf("seed sitename: %v", err)
	}
	if err := migrateAllowRemoteManagement(); err != nil {
		t.Fatalf("migrate upgrade: %v", err)
	}
	enabled, err := config.GetAs[bool](config.AllowRemoteManagementKey)
	if err != nil || !enabled {
		t.Fatalf("upgrade switch = %t, err %v; want true", enabled, err)
	}
}

func TestMigrateAllowRemoteManagementNewInstallStaysOff(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:migrate-remote-new?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open config database: %v", err)
	}
	config.SetDb(db)
	if err := migrateAllowRemoteManagement(); err != nil {
		t.Fatalf("migrate new install: %v", err)
	}
	all, err := config.GetAll()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if _, exists := all[config.AllowRemoteManagementKey]; exists {
		t.Fatal("new install must not persist allow_remote_management; default stays off")
	}
}

func TestMigrateAllowRemoteManagementPreservesExistingValue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:migrate-remote-existing?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open config database: %v", err)
	}
	config.SetDb(db)
	if err := config.SetMany(map[string]any{
		config.SitenameKey:              "Existing Lite",
		config.AllowRemoteManagementKey: false,
	}); err != nil {
		t.Fatalf("seed existing switch: %v", err)
	}
	if err := migrateAllowRemoteManagement(); err != nil {
		t.Fatalf("migrate existing: %v", err)
	}
	enabled, err := config.GetAs[bool](config.AllowRemoteManagementKey)
	if err != nil || enabled {
		t.Fatalf("existing closed switch overwritten: %t, err %v", enabled, err)
	}
}

func TestNormalizeMetricStorageSettingsOverridesLegacyValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:normalize-metric-settings?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open config database: %v", err)
	}
	config.SetDb(db)
	if err := config.SetMany(map[string]any{
		config.LowResourceModeKey:                true,
		metricstore.MetricDownsamplingEnabledKey: false,
		"metric_retention_days":                  37,
	}); err != nil {
		t.Fatalf("seed legacy settings: %v", err)
	}
	if err := normalizeMetricStorageSettings(); err != nil {
		t.Fatalf("normalize storage settings: %v", err)
	}
	lowResource, err := config.GetAs[bool](config.LowResourceModeKey)
	if err != nil || lowResource {
		t.Fatalf("low resource mode = %t, err %v; want false", lowResource, err)
	}
	downsampling, err := config.GetAs[bool](metricstore.MetricDownsamplingEnabledKey)
	if err != nil || !downsampling {
		t.Fatalf("downsampling = %t, err %v; want true", downsampling, err)
	}
	retention, err := config.GetAs[int]("metric_retention_days")
	if err != nil || retention != 37 {
		t.Fatalf("retention = %d, err %v; want preserved value 37", retention, err)
	}
}

func TestStartupMetricCleanupFailureIsRetainedForRetry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:startup-metric-cleanup?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open cleanup database: %v", err)
	}
	if err := db.AutoMigrate(&models.MetricCleanupJob{}); err != nil {
		t.Fatalf("migrate cleanup queue: %v", err)
	}
	job := models.MetricCleanupJob{Kind: "unsupported"}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("seed cleanup job: %v", err)
	}

	processPendingMetricCleanupAtStartup(db)

	var retained models.MetricCleanupJob
	if err := db.First(&retained, job.ID).Error; err != nil {
		t.Fatalf("failed cleanup job was not retained: %v", err)
	}
	if retained.Attempts != 1 || !strings.Contains(retained.LastError, "unsupported metric cleanup kind") {
		t.Fatalf("retained cleanup job = %#v, want one recorded failed attempt", retained)
	}
}

func TestNormalizeSiteFactoryDefaultsMigratesLegacyValuesOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:normalize-site-factory-defaults?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open config database: %v", err)
	}
	config.SetDb(db)
	if err := config.SetMany(map[string]any{
		config.DescriptionKey:          "A simple server monitor tool.",
		config.AdminDefaultPageSizeKey: 10,
		config.ReduceMotionKey:         true,
	}); err != nil {
		t.Fatalf("seed legacy site defaults: %v", err)
	}
	if err := normalizeSiteFactoryDefaults(); err != nil {
		t.Fatalf("normalize site factory defaults: %v", err)
	}
	description, err := config.GetAs[string](config.DescriptionKey)
	if err != nil || description != config.DefaultSiteDescription {
		t.Fatalf("description = %q, err %v; want %q", description, err, config.DefaultSiteDescription)
	}
	pageSize, err := config.GetAs[int](config.AdminDefaultPageSizeKey)
	if err != nil || pageSize != config.AdminDefaultPageSize {
		t.Fatalf("page size = %d, err %v; want %d", pageSize, err, config.AdminDefaultPageSize)
	}
	reduceMotion, err := config.GetAs[bool](config.ReduceMotionKey)
	if err != nil || reduceMotion {
		t.Fatalf("reduce motion = %t, err %v; want false", reduceMotion, err)
	}
	if err := config.SetMany(map[string]any{
		config.DescriptionKey:          "Custom description",
		config.AdminDefaultPageSizeKey: 10,
	}); err != nil {
		t.Fatalf("set customized values: %v", err)
	}
	if err := normalizeSiteFactoryDefaults(); err != nil {
		t.Fatalf("normalize site factory defaults again: %v", err)
	}
	description, err = config.GetAs[string](config.DescriptionKey)
	if err != nil || description != "Custom description" {
		t.Fatalf("custom description overwritten: %q, err %v", description, err)
	}
	pageSize, err = config.GetAs[int](config.AdminDefaultPageSizeKey)
	if err != nil || pageSize != 10 {
		t.Fatalf("custom page size overwritten: %d, err %v", pageSize, err)
	}
}

func TestMetricCompactCycleStateConcurrentAddAndReset(t *testing.T) {
	const workers = 64
	wantErr := errors.New("compact step failed")
	var state metricCompactCycleState
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(failed bool) {
			defer group.Done()
			var err error
			if failed {
				err = wantErr
			}
			state.add(1, err)
		}(i%2 == 0)
	}
	group.Wait()

	written, err := state.finish()
	if written != workers {
		t.Fatalf("cycle buckets = %d, want %d", written, workers)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("cycle error = %v, want joined compact error", err)
	}
	if written, err := state.finish(); written != 0 || err != nil {
		t.Fatalf("reset cycle = buckets %d, err %v; want zero state", written, err)
	}
}
