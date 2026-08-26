package dbcore

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/nuomiiiii/lite/cmd/flags"
	logger "github.com/nuomiiiii/lite/utils/log"
)

const (
	legacyHTTPListenMarker  = ".legacy-http-listen"
	defaultLegacyHTTPListen = "0.0.0.0:25774"
	defaultLiteHTTPListen   = "0.0.0.0:27777"
)

func normalizeDataDir(dataDir string) string {
	if strings.TrimSpace(dataDir) != "" {
		return dataDir
	}
	if strings.TrimSpace(flags.DatabaseFile) != "" {
		dir := filepath.Dir(flags.DatabaseFile)
		if dir != "" && dir != "." {
			return dir
		}
	}
	return filepath.Join(".", "data")
}

// KeepLegacyHTTPListen reports whether this working directory still needs the
// previous HTTP listen address. Command-line -l and LITE_LISTEN / KOMARI_LISTEN
// always win; this only fills the empty default so an in-place upgrade does not
// jump to 27777.
func KeepLegacyHTTPListen(dataDir string) bool {
	dataDir = normalizeDataDir(dataDir)
	if fileExists(filepath.Join(dataDir, "komari.db")) {
		return true
	}
	return fileExists(filepath.Join(dataDir, legacyHTTPListenMarker))
}

func LegacyHTTPListenAddr() string {
	return ResolveDefaultHTTPListen("")
}

// ResolveDefaultHTTPListen returns the HTTP listen address for an unspecified
// -l / LITE_LISTEN. Existing Komari / Komari Lite data keeps the previous
// address (marker contents, otherwise 25774). A true new install uses 27777.
func ResolveDefaultHTTPListen(dataDir string) string {
	dataDir = normalizeDataDir(dataDir)
	if addr, ok := readLegacyHTTPListenMarker(dataDir); ok {
		return addr
	}
	if fileExists(filepath.Join(dataDir, "komari.db")) {
		return defaultLegacyHTTPListen
	}
	return defaultLiteHTTPListen
}

func readLegacyHTTPListenMarker(dataDir string) (string, bool) {
	content, err := os.ReadFile(filepath.Join(dataDir, legacyHTTPListenMarker))
	if err != nil {
		return "", false
	}
	addr, err := normalizeHTTPListenAddr(string(content))
	if err != nil {
		return defaultLegacyHTTPListen, true
	}
	return addr, true
}

func normalizeHTTPListenAddr(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("empty listen address")
	}
	if !strings.Contains(s, ":") {
		s = "0.0.0.0:" + s
	} else if strings.HasPrefix(s, ":") {
		s = "0.0.0.0" + s
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", err
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", err
	}
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, port), nil
}

func writeLegacyHTTPListenMarker(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataDir, legacyHTTPListenMarker)
	if fileExists(path) {
		return nil
	}
	addr := defaultLegacyHTTPListen
	if normalized, err := normalizeHTTPListenAddr(flags.Listen); err == nil && normalized != defaultLiteHTTPListen {
		addr = normalized
	}
	return os.WriteFile(path, []byte(addr+"\n"), 0o600)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func renameSQLitePair(legacy, target string) error {
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(legacy); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := legacy + suffix
		dst := target + suffix
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("rename %s to %s: %w", src, dst, err)
		}
	}
	logger.Infof("dbcore", "adopted Komari Lite database %s as %s", legacy, target)
	return nil
}

func alignStagedMainDatabase(stageDir string) (string, error) {
	targetBase := filepath.Base(resolveDatabaseFile())
	if targetBase == "" || targetBase == "." {
		targetBase = "lite.db"
	}
	target := filepath.Join(stageDir, targetBase)
	if fileExists(target) {
		return target, nil
	}
	for _, alt := range []string{"lite.db", "komari.db"} {
		if strings.EqualFold(alt, targetBase) {
			continue
		}
		altPath := filepath.Join(stageDir, alt)
		if !fileExists(altPath) {
			continue
		}
		if err := renameSQLitePair(altPath, target); err != nil {
			return "", err
		}
		return target, nil
	}
	return target, nil
}

// adoptLegacyKomariSQLite 把 Komari Lite 的 komari.db（含 WAL）改名为 Lite 的 lite.db。
// 仅在目标 lite.db 还不存在、且当前路径文件名就是 lite.db 时执行，避免覆盖已有库。
func adoptLegacyKomariSQLite() error {
	target := resolveDatabaseFile()
	if !strings.EqualFold(filepath.Base(target), "lite.db") {
		return nil
	}
	if fileExists(target) {
		return nil
	}
	legacy := filepath.Join(filepath.Dir(target), "komari.db")
	if !fileExists(legacy) {
		return nil
	}
	if err := renameSQLitePair(legacy, target); err != nil {
		return err
	}
	return writeLegacyHTTPListenMarker(filepath.Dir(target))
}
