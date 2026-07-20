package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DeploymentDocker  = "docker"
	DeploymentLinux   = "linux"
	DeploymentWindows = "windows"
	DeploymentUnknown = "unknown"
	defaultService    = "komari.service"
)

type Capability struct {
	Deployment          string        `json:"deployment"`
	Distribution        string        `json:"distribution,omitempty"`
	DistributionVersion string        `json:"distribution_version,omitempty"`
	Supported           bool          `json:"supported"`
	Reason              string        `json:"reason,omitempty"`
	Service             string        `json:"service,omitempty"`
	LastResult          *UpdateResult `json:"last_result,omitempty"`
}

func DeploymentType() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KOMARI_DEPLOYMENT"))) {
	case DeploymentDocker:
		return DeploymentDocker
	case DeploymentLinux, "binary":
		return DeploymentLinux
	case DeploymentWindows:
		return DeploymentWindows
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return DeploymentDocker
	}
	if runtime.GOOS == "linux" {
		return DeploymentLinux
	}
	if runtime.GOOS == "windows" {
		return DeploymentWindows
	}
	return DeploymentUnknown
}

func DetectCapability() Capability {
	deployment := DeploymentType()
	result := Capability{Deployment: deployment}
	result.Distribution, result.DistributionVersion = linuxDistribution()
	if deployment != DeploymentLinux || runtime.GOOS != "linux" {
		result.Reason = "not_managed_linux"
		return result
	}
	if output, err := exec.Command("id", "-u").Output(); err != nil || strings.TrimSpace(string(output)) != "0" {
		result.Reason = "root_required"
		return result
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		result.Reason = "systemd_unavailable"
		return result
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		result.Reason = "systemd_run_unavailable"
		return result
	}

	service := serviceName()
	result.Service = service
	output, err := exec.Command("systemctl", "show", service, "--property=MainPID").Output()
	mainPID := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(output)), "MainPID="))
	if err != nil || mainPID != strconv.Itoa(os.Getpid()) {
		result.Reason = "service_mismatch"
		return result
	}

	executable, dataDir, err := managedPaths()
	if err != nil {
		result.Reason = "unsupported_layout"
		return result
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		result.Reason = "executable_unavailable"
		return result
	}
	if info, err := os.Lstat(dataDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		result.Reason = "data_directory_unavailable"
		return result
	}
	if isMountPoint(dataDir) {
		result.Reason = "data_directory_is_mount"
		return result
	}

	result.Supported = true
	result.LastResult, _ = ReadLastResult(filepath.Dir(executable))
	return result
}

func linuxDistribution() (string, string) {
	content, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", ""
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values["ID"], values["VERSION_ID"]
}

func managedPaths() (string, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return "", "", err
	}
	dataDir := filepath.Join(workingDir, "data")
	if !pathWithin(dataDir, workingDir) || filepath.Clean(dataDir) == filepath.Clean(workingDir) {
		return "", "", errors.New("invalid data directory")
	}
	if !configuredDatabaseWithin(dataDir, workingDir) {
		return "", "", errors.New("database is outside the managed data directory")
	}
	return executable, dataDir, nil
}

func configuredDatabaseWithin(dataDir, workingDir string) bool {
	databasePath := "./data/komari.db"
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "-d" || arg == "--database":
			if i+1 < len(os.Args) {
				databasePath = os.Args[i+1]
			}
		case strings.HasPrefix(arg, "--database="):
			databasePath = strings.TrimPrefix(arg, "--database=")
		}
	}
	if strings.HasPrefix(strings.ToLower(databasePath), "file:") {
		databasePath = strings.TrimPrefix(databasePath, "file:")
		if index := strings.IndexByte(databasePath, '?'); index >= 0 {
			databasePath = databasePath[:index]
		}
	}
	if !filepath.IsAbs(databasePath) {
		databasePath = filepath.Join(workingDir, databasePath)
	}
	return pathWithin(databasePath, dataDir)
}

func PathWithinData(path, dataDir, workingDir string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "./data/metrics.db"
	}
	if strings.HasPrefix(strings.ToLower(path), "file:") {
		path = strings.TrimPrefix(path, "file:")
		if index := strings.IndexByte(path, '?'); index >= 0 {
			path = path[:index]
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	return pathWithin(path, dataDir)
}

func pathWithin(path, parent string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absParent, absPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func serviceName() string {
	name := strings.TrimSpace(os.Getenv("KOMARI_SERVICE_NAME"))
	if name == "" {
		return defaultService
	}
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	return name
}

func localHealthURL() (string, error) {
	listen := strings.TrimSpace(os.Getenv("KOMARI_LISTEN"))
	if listen == "" {
		listen = "0.0.0.0:25774"
	}
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "-l" || arg == "--listen":
			if i+1 < len(os.Args) {
				listen = os.Args[i+1]
			}
		case strings.HasPrefix(arg, "--listen="):
			listen = strings.TrimPrefix(arg, "--listen=")
		}
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("unsupported listen address %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/version", nil
}

func isMountPoint(path string) bool {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	clean := filepath.Clean(path)
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && filepath.Clean(strings.ReplaceAll(fields[4], `\040`, " ")) == clean {
			return true
		}
	}
	return false
}

func ReadLastResult(executableDir string) (*UpdateResult, error) {
	content, err := os.ReadFile(filepath.Join(executableDir, updateRootName, lastResultName))
	if err != nil {
		return nil, err
	}
	var result UpdateResult
	if err := json.Unmarshal(content, &result); err != nil {
		return nil, err
	}
	if time.Since(result.UpdatedAt) > 7*24*time.Hour {
		return nil, nil
	}
	return &result, nil
}
