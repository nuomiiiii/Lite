package thememanifest

import (
	"os"
	"path"
	"path/filepath"
)

const (
	File       = "Lite-theme.json"
	LegacyFile = "komari-theme.json"
)

func Names() []string {
	return []string{File, LegacyFile}
}

func MissingMessage() string {
	return "主题配置文件 " + File + " 或 " + LegacyFile + " 不存在"
}

// RootName returns File or LegacyFile when name is a root-level theme manifest.
func RootName(name string) string {
	clean := path.Clean(filepath.ToSlash(name))
	if clean == File || clean == LegacyFile {
		return clean
	}
	return ""
}

// FindInDir returns the first existing manifest path, preferring File.
func FindInDir(dir string) (string, bool) {
	for _, name := range Names() {
		manifestPath := filepath.Join(dir, name)
		info, err := os.Stat(manifestPath)
		if err == nil && !info.IsDir() {
			return manifestPath, true
		}
	}
	return "", false
}
