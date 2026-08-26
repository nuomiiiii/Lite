package dbcore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPreserveLegacyHTTPSListenFillsMissingKey(t *testing.T) {
	dir := t.TempDir()
	previousDB := flags.DatabaseFile
	previousInstance := instance
	t.Cleanup(func() {
		flags.DatabaseFile = previousDB
		instance = previousInstance
	})
	flags.DatabaseFile = filepath.Join(dir, "lite.db")
	if err := os.WriteFile(filepath.Join(dir, ".legacy-http-listen"), []byte("0.0.0.0:25774\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	instance = db
	config.SetDb(db)

	if err := preserveLegacyHTTPSListen(); err != nil {
		t.Fatal(err)
	}
	got, err := config.GetAs[string](config.HTTPSListenKey)
	if err != nil {
		t.Fatal(err)
	}
	if got != ":35938" {
		t.Fatalf("missing HTTPS listen filled with %q, want :35938", got)
	}

	if err := config.Set(config.HTTPSListenKey, ":36888"); err != nil {
		t.Fatal(err)
	}
	if err := preserveLegacyHTTPSListen(); err != nil {
		t.Fatal(err)
	}
	got, err = config.GetAs[string](config.HTTPSListenKey)
	if err != nil {
		t.Fatal(err)
	}
	if got != ":36888" {
		t.Fatalf("existing HTTPS listen overwritten: %q", got)
	}
}

func TestPreserveLegacyHTTPSListenSkipsNewInstall(t *testing.T) {
	dir := t.TempDir()
	previousDB := flags.DatabaseFile
	previousInstance := instance
	t.Cleanup(func() {
		flags.DatabaseFile = previousDB
		instance = previousInstance
	})
	flags.DatabaseFile = filepath.Join(dir, "lite.db")

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	instance = db
	config.SetDb(db)

	if err := preserveLegacyHTTPSListen(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&config.ConfigItem{}).Where("key = ?", config.HTTPSListenKey).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("new install must not pre-fill HTTPS listen")
	}
}
