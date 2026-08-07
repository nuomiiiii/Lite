package public

import (
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/komari-monitor/komari/pkg/config"
)

const (
	themeServerUUIDPlaceholder = "{uuid}"
	themeServerIDPlaceholder   = "{id}"
)

type ThemeNavigation struct {
	serverDetailTemplate string
	pingTaskParameter    string
}

type themeNavigationManifest struct {
	Navigation struct {
		ServerDetail      string `json:"server_detail"`
		PingTaskParameter string `json:"ping_task_parameter"`
	} `json:"navigation"`
}

func ActiveThemeNavigation() ThemeNavigation {
	themeID, _ := config.GetAs[string](config.ThemeKey, DefaultTheme)
	if manifest, _, ok := localThemeFileContent(themeID, "komari-theme.json"); ok {
		if navigation, valid := parseThemeNavigation(manifest); valid {
			return navigation
		}
	}
	return bundledThemeNavigation(themeID)
}

func (navigation ThemeNavigation) ServerDetailURL(uuid string, taskID uint) string {
	if !validThemeServerDetailTemplate(navigation.serverDetailTemplate) || strings.TrimSpace(uuid) == "" {
		return "/"
	}
	target := navigation.serverDetailTemplate
	if strings.Contains(target, themeServerUUIDPlaceholder) {
		target = strings.Replace(target, themeServerUUIDPlaceholder, url.PathEscape(uuid), 1)
	} else {
		target = strings.Replace(target, themeServerIDPlaceholder, strconv.FormatUint(uint64(themeServerNumericID(uuid)), 10), 1)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return "/"
	}
	if taskID > 0 && validThemeQueryParameter(navigation.pingTaskParameter) {
		query := parsed.Query()
		query.Set(navigation.pingTaskParameter, strconv.FormatUint(uint64(taskID), 10))
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func parseThemeNavigation(data []byte) (ThemeNavigation, bool) {
	var manifest themeNavigationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ThemeNavigation{}, false
	}
	navigation := ThemeNavigation{
		serverDetailTemplate: strings.TrimSpace(manifest.Navigation.ServerDetail),
		pingTaskParameter:    strings.TrimSpace(manifest.Navigation.PingTaskParameter),
	}
	if !validThemeServerDetailTemplate(navigation.serverDetailTemplate) {
		return ThemeNavigation{}, false
	}
	if navigation.pingTaskParameter != "" && !validThemeQueryParameter(navigation.pingTaskParameter) {
		navigation.pingTaskParameter = ""
	}
	return navigation, true
}

func bundledThemeNavigation(themeID string) ThemeNavigation {
	switch strings.TrimSpace(themeID) {
	case DefaultTheme:
		return ThemeNavigation{serverDetailTemplate: "/server/{uuid}", pingTaskParameter: "ping_task"}
	case ClassicTheme, LegacyDefaultTheme:
		return ThemeNavigation{serverDetailTemplate: "/instance/{uuid}"}
	default:
		// Existing Komari themes traditionally use /instance/:uuid. Themes with
		// another route can declare it explicitly in komari-theme.json.
		return ThemeNavigation{serverDetailTemplate: "/instance/{uuid}"}
	}
}

func validThemeServerDetailTemplate(template string) bool {
	placeholderCount := strings.Count(template, themeServerUUIDPlaceholder) + strings.Count(template, themeServerIDPlaceholder)
	if placeholderCount != 1 || strings.Contains(template, "\\") {
		return false
	}
	probe := strings.Replace(template, themeServerUUIDPlaceholder, "node", 1)
	probe = strings.Replace(probe, themeServerIDPlaceholder, "123", 1)
	parsed, err := url.Parse(probe)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return strings.HasPrefix(parsed.Path, "/") && path.Clean(parsed.Path) == parsed.Path
}

// themeServerNumericID matches the unsigned 32-bit JavaScript hash used by
// legacy Nezha-style themes for their numeric server route.
func themeServerNumericID(uuid string) uint32 {
	var hash uint32
	for _, codeUnit := range utf16.Encode([]rune(uuid)) {
		hash = uint32(codeUnit) + (hash << 5) - hash
	}
	return hash
}

func validThemeQueryParameter(parameter string) bool {
	if parameter == "" {
		return false
	}
	for _, character := range parameter {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
