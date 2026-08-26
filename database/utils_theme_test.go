package database

import (
	"strings"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateLiteThemeConfigurationCopiesLegacySettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ThemeConfiguration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ThemeConfiguration{
		Short: "nezha",
		Data:  `{"GroupOrder":["asia"],"CustomLogo":"/logo.png"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateLiteThemeConfiguration(db); err != nil {
		t.Fatal(err)
	}
	var target models.ThemeConfiguration
	if err := db.Where("short = ?", liteThemeShort).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target.Data, "GroupOrder") || !strings.Contains(target.Data, "/logo.png") {
		t.Fatalf("legacy theme settings were not copied: %s", target.Data)
	}
}

func TestMigrateLiteThemeConfigurationKeepsExistingLiteSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ThemeConfiguration{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ThemeConfiguration{
		Short: liteThemeShort,
		Data:  `{"CustomLogo":"keep-me"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ThemeConfiguration{
		Short: "nezha",
		Data:  `{"CustomLogo":"overwrite-me"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateLiteThemeConfiguration(db); err != nil {
		t.Fatal(err)
	}
	var target models.ThemeConfiguration
	if err := db.Where("short = ?", liteThemeShort).First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if target.Data != `{"CustomLogo":"keep-me"}` {
		t.Fatalf("existing Lite-Theme settings were overwritten: %s", target.Data)
	}
}
