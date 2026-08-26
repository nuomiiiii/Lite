package database

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/managedconfig"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/thememanifest"
	logger "github.com/nuomiiiii/lite/utils/log"
	"gorm.io/gorm"
)

const liteThemeShort = "lite-theme"

func migrateLiteThemeConfiguration(db *gorm.DB) error {
	var target models.ThemeConfiguration
	if result := db.Where("short = ?", liteThemeShort).Limit(1).Find(&target); result.Error != nil {
		return result.Error
	}
	if strings.TrimSpace(target.Data) != "" && strings.TrimSpace(target.Data) != "{}" {
		return nil
	}

	for _, legacyShort := range []string{"lite-theme-default", "nezha"} {
		var source models.ThemeConfiguration
		result := db.Where("short = ?", legacyShort).Limit(1).Find(&source)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 || strings.TrimSpace(source.Data) == "" {
			continue
		}

		var legacySettings map[string]any
		if err := json.Unmarshal([]byte(source.Data), &legacySettings); err != nil {
			continue
		}
		data, err := json.Marshal(legacySettings)
		if err != nil {
			return err
		}
		target = models.ThemeConfiguration{Short: liteThemeShort, Data: string(data)}
		return db.Where("short = ?", liteThemeShort).Assign(target).FirstOrCreate(&target).Error
	}
	return nil
}

func GetPublicInfo() (map[string]interface{}, error) {
	cstPtr, err := config.GetManyAs[config.Settings]()
	if err != nil {
		return nil, err
	}
	cst := *cstPtr

	all, allErr := config.GetAll()
	hasKey := func(k string) bool {
		if allErr != nil {
			return false
		}
		_, ok := all[k]
		return ok
	}

	// Apply defaults only when a key is missing.
	if !hasKey("sitename") {
		cst.Sitename = "Lite"
	}
	if !hasKey("description") {
		cst.Description = config.DefaultSiteDescription
	}
	if !hasKey("theme") {
		cst.Theme = "lite-theme"
	}
	if !hasKey("o_auth_provider") {
		cst.OAuthProvider = "github"
	}

	// Fallback defaults if we couldn't enumerate keys.
	if allErr != nil {
		if cst.Sitename == "" {
			cst.Sitename = "Lite"
		}
		if cst.Description == "" {
			cst.Description = config.DefaultSiteDescription
		}
	}
	retention, err := metricstore.GetRetentionSummary(context.Background())
	if err != nil {
		return nil, err
	}
	db := dbcore.GetDBInstance()
	if cst.Theme == liteThemeShort {
		if err := migrateLiteThemeConfiguration(db); err != nil {
			return nil, err
		}
	}
	tc := models.ThemeConfiguration{}
	err = db.Model(&models.ThemeConfiguration{}).Where("short = ?", cst.Theme).First(&tc).Error
	if err != nil {
		tc.Data = "{}"
	}
	tc_data := gin.H{}
	err = json.Unmarshal([]byte(tc.Data), &tc_data)
	if err != nil {
		logger.Infof("database", "%v", err)
	}
	// Try to load theme declaration file and merge defaults for managed configuration
	// Prefer Lite-theme.json, then komari-theme.json for installed Komari packages.
	if cst.Theme != "" && cst.Theme != "default" {
		themeConfigPath, ok := thememanifest.FindInDir(filepath.Join("./data/theme", cst.Theme))
		if ok {
			b, err := os.ReadFile(themeConfigPath)
			if err == nil {
				var themeDecl struct {
					Configuration struct {
						Type string                                 `json:"type"`
						Data []models.ManagedThemeConfigurationItem `json:"data"`
					} `json:"configuration"`
				}
				if err := json.Unmarshal(b, &themeDecl); err == nil {
					if themeDecl.Configuration.Type == "managed" {
						for _, item := range themeDecl.Configuration.Data {
							if item.Key == "" {
								continue
							}
							// missing
							if _, exists := tc_data[item.Key]; !exists {
								tc_data[item.Key] = managedconfig.DefaultValue(item)
							}
						}
						if err := managedconfig.ResolveForOutput(db, tc_data, themeDecl.Configuration.Data); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}

	return gin.H{
		"sitename":                  cst.Sitename,
		"description":               cst.Description,
		"custom_head":               cst.CustomHead,
		"custom_body":               cst.CustomBody,
		"oauth_enable":              cst.OAuthEnabled,
		"oauth_provider":            cst.OAuthProvider,
		"disable_password_login":    cst.DisablePasswordLogin,
		"cors_origin_check_enabled": cst.CorsOriginCheckEnabled,
		"record_enabled":            retention.AllPositive, // 兼容旧版本主题
		"record_preserve_time":      retention.MaxDays * 24,
		"ping_record_preserve_time": retention.MaxDays * 24,
		"private_site":              cst.PrivateSite,
		"visitor_audit_enabled":     cst.VisitorAuditEnabled,
		"theme":                     cst.Theme,
		"theme_settings":            tc_data,
	}, nil
}
