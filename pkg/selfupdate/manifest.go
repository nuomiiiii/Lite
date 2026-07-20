package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	releaseBaseURL = "https://github.com/nuomiiiii/komari/releases/download"
	manifestName   = "komari-update.json"
	maxManifest    = 2 << 20
	maxBinary      = 256 << 20
)

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	hashPattern    = regexp.MustCompile(`^[A-Za-z0-9]{7}$`)
)

type Manifest struct {
	Schema      int             `json:"schema"`
	Version     string          `json:"version"`
	VersionHash string          `json:"version_hash"`
	Assets      []ManifestAsset `json:"assets"`
}

type ManifestAsset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func fetchManifest(ctxURL string, client *http.Client) (*Manifest, error) {
	req, err := http.NewRequest(http.MethodGet, ctxURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update manifest returned HTTP %d", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxManifest {
		return nil, errors.New("update manifest is too large")
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return nil, fmt.Errorf("invalid update manifest: %w", err)
	}
	return &manifest, nil
}

func (manifest *Manifest) validate(version, versionHash string) (*ManifestAsset, error) {
	if manifest.Schema != 1 {
		return nil, fmt.Errorf("unsupported update manifest schema %d", manifest.Schema)
	}
	if manifest.Version != version || !strings.EqualFold(manifest.VersionHash, versionHash) {
		return nil, errors.New("release metadata does not match the requested update")
	}
	name := fmt.Sprintf("komari-%s-%s", runtime.GOOS, runtime.GOARCH)
	for i := range manifest.Assets {
		asset := &manifest.Assets[i]
		if asset.Name != name || asset.OS != runtime.GOOS || asset.Arch != runtime.GOARCH {
			continue
		}
		if asset.Size <= 0 || asset.Size > maxBinary {
			return nil, errors.New("invalid update asset size")
		}
		if len(asset.SHA256) != sha256.Size*2 {
			return nil, errors.New("invalid update asset checksum")
		}
		if _, err := hex.DecodeString(asset.SHA256); err != nil {
			return nil, errors.New("invalid update asset checksum")
		}
		return asset, nil
	}
	return nil, fmt.Errorf("release does not contain %s", name)
}

func downloadAsset(client *http.Client, url, destination string, asset ManifestAsset) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update asset returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 && resp.ContentLength != asset.Size {
		return errors.New("update asset size does not match the manifest")
	}

	temporary := destination + ".download"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(resp.Body, asset.Size+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != asset.Size {
		_ = os.Remove(temporary)
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return errors.New("update asset size does not match the manifest")
	}
	if !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), asset.SHA256) {
		_ = os.Remove(temporary)
		return errors.New("update asset checksum verification failed")
	}
	if err := os.Chmod(temporary, 0755); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, destination)
}

func releaseURL(version, name string) string {
	return releaseBaseURL + "/" + version + "/" + filepath.Base(name)
}

func updateHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Minute}
}
