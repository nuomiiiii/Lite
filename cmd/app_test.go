package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/pkg/config"
	installweb "github.com/komari-monitor/komari/web/install"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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
