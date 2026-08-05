package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultReportInterval = 3.0

// DeploymentProfile contains both runtime-manageable settings and values that
// are retained solely to regenerate an installation command.
type DeploymentProfile struct {
	Platform                 string  `json:"platform"`
	DisableWebSSH            bool    `json:"disable_web_ssh"`
	DisableAutoUpdate        bool    `json:"disable_auto_update"`
	IgnoreUnsafeCert         bool    `json:"ignore_unsafe_cert"`
	GetIPAddrFromNIC         bool    `json:"get_ip_addr_from_nic"`
	MemoryIncludeCache       bool    `json:"memory_include_cache"`
	EnableGPU                bool    `json:"enable_gpu"`
	EnableGHProxy            bool    `json:"enable_ghproxy"`
	GHProxy                  string  `json:"ghproxy"`
	EnableCustomDir          bool    `json:"enable_custom_dir"`
	Dir                      string  `json:"dir"`
	EnableCustomServiceName  bool    `json:"enable_custom_service_name"`
	ServiceName              string  `json:"service_name"`
	EnableIncludeNics        bool    `json:"enable_include_nics"`
	IncludeNics              string  `json:"include_nics"`
	EnableExcludeNics        bool    `json:"enable_exclude_nics"`
	ExcludeNics              string  `json:"exclude_nics"`
	EnableIncludeMountpoints bool    `json:"enable_include_mountpoints"`
	IncludeMountpoints       string  `json:"include_mountpoints"`
	EnableInterval           bool    `json:"enable_interval"`
	Interval                 float64 `json:"interval"`
	EnableMonthRotate        bool    `json:"enable_month_rotate"`
	MonthRotate              int     `json:"month_rotate"`
}

func defaultDeploymentProfile(client models.Client) DeploymentProfile {
	profile := DeploymentProfile{Platform: "linux"}
	if client.TrafficResetDay != nil && *client.TrafficResetDay > 0 {
		profile.EnableMonthRotate = true
		profile.MonthRotate = *client.TrafficResetDay
	}
	return profile
}

func GetDeploymentProfile(clientUUID string) (DeploymentProfile, bool, error) {
	return getDeploymentProfile(dbcore.GetDBInstance(), clientUUID)
}

func getDeploymentProfile(db *gorm.DB, clientUUID string) (DeploymentProfile, bool, error) {
	var client models.Client
	if err := db.Select("uuid", "traffic_reset_day").First(&client, "uuid = ?", clientUUID).Error; err != nil {
		return DeploymentProfile{}, false, err
	}

	profile := defaultDeploymentProfile(client)
	var stored models.ClientDeploymentProfile
	if err := db.First(&stored, "client = ?", clientUUID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return profile, false, nil
		}
		return DeploymentProfile{}, false, err
	}
	if err := json.Unmarshal([]byte(stored.Config), &profile); err != nil {
		return DeploymentProfile{}, false, fmt.Errorf("decode deployment profile: %w", err)
	}
	if err := normalizeDeploymentProfile(&profile); err != nil {
		return DeploymentProfile{}, false, fmt.Errorf("validate stored deployment profile: %w", err)
	}
	// The billing editor can also change this value, so the client column is
	// authoritative for monthly rotation.
	profile.EnableMonthRotate = client.TrafficResetDay != nil && *client.TrafficResetDay > 0
	if profile.EnableMonthRotate {
		profile.MonthRotate = *client.TrafficResetDay
	} else {
		profile.MonthRotate = 0
	}
	return profile, true, nil
}

func SaveDeploymentProfile(clientUUID string, profile DeploymentProfile) (DeploymentProfile, error) {
	return saveDeploymentProfile(dbcore.GetDBInstance(), clientUUID, profile)
}

func saveDeploymentProfile(db *gorm.DB, clientUUID string, profile DeploymentProfile) (DeploymentProfile, error) {
	if err := normalizeDeploymentProfile(&profile); err != nil {
		return DeploymentProfile{}, err
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return DeploymentProfile{}, fmt.Errorf("encode deployment profile: %w", err)
	}
	resetDay := 0
	if profile.EnableMonthRotate {
		resetDay = profile.MonthRotate
	}
	now := time.Now().UTC()
	row := models.ClientDeploymentProfile{
		Client: clientUUID,
		Config: string(encoded),
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var client models.Client
		if err := tx.Select("uuid").First(&client, "uuid = ?", clientUUID).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.Client{}).Where("uuid = ?", clientUUID).Update("traffic_reset_day", resetDay).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}},
			DoUpdates: clause.Assignments(map[string]any{"config": row.Config, "updated_at": now}),
		}).Create(&row).Error
	})
	if err != nil {
		return DeploymentProfile{}, err
	}
	return profile, nil
}

func normalizeDeploymentProfile(profile *DeploymentProfile) error {
	profile.Platform = strings.ToLower(strings.TrimSpace(profile.Platform))
	switch profile.Platform {
	case "linux", "windows", "macos", "docker":
	default:
		return fmt.Errorf("platform must be linux, windows, macos, or docker")
	}

	var err error
	if profile.GHProxy, err = normalizeProfileText("ghproxy", profile.GHProxy, 2048); err != nil {
		return err
	}
	if profile.Dir, err = normalizeProfileText("dir", profile.Dir, 1024); err != nil {
		return err
	}
	if profile.ServiceName, err = normalizeProfileText("service_name", profile.ServiceName, 128); err != nil {
		return err
	}
	if profile.IncludeNics, err = normalizeProfileText("include_nics", profile.IncludeNics, 1024); err != nil {
		return err
	}
	if profile.ExcludeNics, err = normalizeProfileText("exclude_nics", profile.ExcludeNics, 1024); err != nil {
		return err
	}
	if profile.IncludeMountpoints, err = normalizeProfileText("include_mountpoints", profile.IncludeMountpoints, 2048); err != nil {
		return err
	}

	if profile.EnableGHProxy && profile.GHProxy != "" {
		parsed, parseErr := url.Parse(profile.GHProxy)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("ghproxy must be an http or https URL")
		}
	}
	if profile.EnableCustomServiceName && profile.ServiceName != "" {
		for _, r := range profile.ServiceName {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.@-", r)) {
				return fmt.Errorf("service_name contains an unsupported character")
			}
		}
	}
	if !profile.EnableIncludeNics {
		profile.IncludeNics = ""
	}
	if !profile.EnableExcludeNics {
		profile.ExcludeNics = ""
	}
	if !profile.EnableIncludeMountpoints {
		profile.IncludeMountpoints = ""
	}
	if profile.EnableInterval {
		if profile.Interval < 1 || profile.Interval > 3600 {
			return fmt.Errorf("interval must be between 1 and 3600 seconds")
		}
	} else {
		profile.Interval = 0
	}
	if profile.EnableMonthRotate {
		if profile.MonthRotate < 1 || profile.MonthRotate > 31 {
			return fmt.Errorf("month_rotate must be a day from 1 to 31")
		}
	} else {
		profile.MonthRotate = 0
	}
	return nil
}

func normalizeProfileText(field, value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > maxLength {
		return "", fmt.Errorf("%s is too long", field)
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("%s must not contain control characters", field)
	}
	return value, nil
}

// RuntimeConfig intentionally contains none of the seven installation-only
// options retained by DeploymentProfile.
func (profile DeploymentProfile) RuntimeConfig() v2.ConfigParams {
	interval := defaultReportInterval
	if profile.EnableInterval {
		interval = profile.Interval
	}
	monthRotate := 0
	if profile.EnableMonthRotate {
		monthRotate = profile.MonthRotate
	}
	includeNics := ""
	if profile.EnableIncludeNics {
		includeNics = profile.IncludeNics
	}
	excludeNics := ""
	if profile.EnableExcludeNics {
		excludeNics = profile.ExcludeNics
	}
	includeMountpoints := ""
	if profile.EnableIncludeMountpoints {
		includeMountpoints = profile.IncludeMountpoints
	}
	memoryIncludeCache := profile.MemoryIncludeCache
	enableGPU := profile.EnableGPU
	return v2.ConfigParams{
		MonthRotate:        &monthRotate,
		Interval:           &interval,
		IncludeNics:        &includeNics,
		ExcludeNics:        &excludeNics,
		IncludeMountpoints: &includeMountpoints,
		MemoryIncludeCache: &memoryIncludeCache,
		EnableGPU:          &enableGPU,
	}
}

func deploymentProfileFromRuntime(platform string, config v2.ConfigParams) (DeploymentProfile, error) {
	profile := DeploymentProfile{Platform: deploymentPlatformFromAgent(platform)}
	if config.MonthRotate != nil {
		if *config.MonthRotate < 0 || *config.MonthRotate > 31 {
			return DeploymentProfile{}, fmt.Errorf("month_rotate must be 0 or a day from 1 to 31")
		}
		profile.EnableMonthRotate = *config.MonthRotate > 0
		profile.MonthRotate = *config.MonthRotate
	}
	if config.Interval != nil {
		if *config.Interval < 1 || *config.Interval > 3600 {
			return DeploymentProfile{}, fmt.Errorf("interval must be between 1 and 3600 seconds")
		}
		profile.EnableInterval = *config.Interval != defaultReportInterval
		profile.Interval = *config.Interval
	}
	if config.IncludeNics != nil {
		profile.IncludeNics = *config.IncludeNics
		profile.EnableIncludeNics = strings.TrimSpace(*config.IncludeNics) != ""
	}
	if config.ExcludeNics != nil {
		profile.ExcludeNics = *config.ExcludeNics
		profile.EnableExcludeNics = strings.TrimSpace(*config.ExcludeNics) != ""
	}
	if config.IncludeMountpoints != nil {
		profile.IncludeMountpoints = *config.IncludeMountpoints
		profile.EnableIncludeMountpoints = strings.TrimSpace(*config.IncludeMountpoints) != ""
	}
	if config.MemoryIncludeCache != nil {
		profile.MemoryIncludeCache = *config.MemoryIncludeCache
	}
	if config.EnableGPU != nil {
		profile.EnableGPU = *config.EnableGPU
	}
	if err := normalizeDeploymentProfile(&profile); err != nil {
		return DeploymentProfile{}, err
	}
	return profile, nil
}

func deploymentPlatformFromAgent(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "windows":
		return "windows"
	case "darwin", "macos":
		return "macos"
	case "docker":
		return "docker"
	default:
		return "linux"
	}
}

// AdoptDeploymentRuntimeConfig initializes an unmanaged node from the
// effective settings reported by Agent. Once a profile exists, Komari remains
// authoritative and a stale Agent report cannot overwrite an administrator's
// saved configuration.
func AdoptDeploymentRuntimeConfig(clientUUID, platform string, config v2.ConfigParams) (bool, error) {
	return adoptDeploymentRuntimeConfig(dbcore.GetDBInstance(), clientUUID, platform, config)
}

func adoptDeploymentRuntimeConfig(db *gorm.DB, clientUUID, platform string, config v2.ConfigParams) (bool, error) {
	reported, err := deploymentProfileFromRuntime(platform, config)
	if err != nil {
		return false, err
	}
	adopted := false
	err = db.Transaction(func(tx *gorm.DB) error {
		var client models.Client
		if err := tx.Select("uuid", "traffic_reset_day").First(&client, "uuid = ?", clientUUID).Error; err != nil {
			return err
		}
		if client.TrafficResetDay != nil {
			reported.EnableMonthRotate = *client.TrafficResetDay > 0
			reported.MonthRotate = *client.TrafficResetDay
		}
		if err := normalizeDeploymentProfile(&reported); err != nil {
			return err
		}
		encoded, err := json.Marshal(reported)
		if err != nil {
			return fmt.Errorf("encode reported deployment profile: %w", err)
		}
		row := models.ClientDeploymentProfile{Client: clientUUID, Config: string(encoded)}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		adopted = true
		if client.TrafficResetDay == nil {
			resetDay := 0
			if reported.EnableMonthRotate {
				resetDay = reported.MonthRotate
			}
			if err := tx.Model(&models.Client{}).Where("uuid = ?", clientUUID).
				Update("traffic_reset_day", resetDay).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return adopted, err
}
