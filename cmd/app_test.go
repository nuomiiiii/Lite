package cmd

import (
	"errors"
	"sync"
	"testing"

	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

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
