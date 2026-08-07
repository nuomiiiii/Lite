package public

import (
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/komari-monitor/komari/pkg/config"
)

const (
	themeServerUUIDPlaceholder = "{uuid}"
)

type ThemeNavigation struct {
	serverDetailTemplate  string
	serverNetworkTemplate string
	pingTaskParameter     string
}

type themeNavigationManifest struct {
	Navigation struct {
		ServerDetail      string `json:"server_detail"`
		ServerNetwork     string `json:"server_network"`
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
	return navigation.serverURL(navigation.serverDetailTemplate, uuid, taskID)
}

func (navigation ThemeNavigation) ServerNetworkURL(uuid string) string {
	if validThemeServerRouteTemplate(navigation.serverNetworkTemplate, true) {
		return navigation.serverURL(navigation.serverNetworkTemplate, uuid, 0)
	}
	return navigation.ServerDetailURL(uuid, 0)
}

func (navigation ThemeNavigation) serverURL(template, uuid string, taskID uint) string {
	if !validThemeServerRouteTemplate(template, true) || strings.TrimSpace(uuid) == "" {
		return "/"
	}
	target := template
	target = strings.Replace(target, themeServerUUIDPlaceholder, url.PathEscape(uuid), 1)
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
		serverDetailTemplate:  strings.TrimSpace(manifest.Navigation.ServerDetail),
		serverNetworkTemplate: strings.TrimSpace(manifest.Navigation.ServerNetwork),
		pingTaskParameter:     strings.TrimSpace(manifest.Navigation.PingTaskParameter),
	}
	if !validThemeServerDetailTemplate(navigation.serverDetailTemplate) {
		return ThemeNavigation{}, false
	}
	if navigation.serverNetworkTemplate != "" && !validThemeServerRouteTemplate(navigation.serverNetworkTemplate, true) {
		navigation.serverNetworkTemplate = ""
	}
	if navigation.pingTaskParameter != "" && !validThemeQueryParameter(navigation.pingTaskParameter) {
		navigation.pingTaskParameter = ""
	}
	return navigation, true
}

func bundledThemeNavigation(themeID string) ThemeNavigation {
	switch strings.TrimSpace(themeID) {
	case DefaultTheme:
		return ThemeNavigation{
			serverDetailTemplate:  "/server/{uuid}",
			serverNetworkTemplate: "/server/{uuid}?view=network",
			pingTaskParameter:     "ping_task",
		}
	case ClassicTheme, LegacyDefaultTheme:
		return ThemeNavigation{serverDetailTemplate: "/instance/{uuid}"}
	default:
		// Existing Komari themes traditionally use /instance/:uuid. Themes with
		// another route can declare it explicitly in komari-theme.json.
		return ThemeNavigation{serverDetailTemplate: "/instance/{uuid}"}
	}
}

func validThemeServerDetailTemplate(template string) bool {
	return validThemeServerRouteTemplate(template, false)
}

func validThemeServerRouteTemplate(template string, allowQuery bool) bool {
	if strings.Count(template, themeServerUUIDPlaceholder) != 1 || strings.Contains(template, "\\") {
		return false
	}
	probe := strings.Replace(template, themeServerUUIDPlaceholder, "node", 1)
	if strings.ContainsAny(probe, "{}") {
		return false
	}
	parsed, err := url.Parse(probe)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || (!allowQuery && parsed.RawQuery != "") || parsed.Fragment != "" {
		return false
	}
	return strings.HasPrefix(parsed.Path, "/") && path.Clean(parsed.Path) == parsed.Path
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
