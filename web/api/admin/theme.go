package admin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/themehttp"
	"github.com/nuomiiiii/lite/pkg/thememanifest"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/public"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxThemeArchiveFiles  = 10000
	maxThemeArchiveSize   = themehttp.MaxArchive
	maxThemeFileSize      = 128 << 20
	maxThemeExtractedSize = 512 << 20
	maxThemeManifestSize  = 1 << 20
)

var (
	themeMutationMu         sync.Mutex
	errThemeArchiveTooLarge = errors.New("theme archive too large")
)

func temporaryThemeArchive(data []byte, prefix string) (string, error) {
	file, err := os.CreateTemp("", prefix+"-*.zip")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func installedThemes() ([]models.Theme, error) {
	entries, err := os.ReadDir("./data/theme")
	if os.IsNotExist(err) {
		return []models.Theme{}, nil
	}
	if err != nil {
		return nil, err
	}
	themes := make([]models.Theme, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !public.IsLocalThemeUsable(entry.Name()) {
			continue
		}
		themePath, ok := thememanifest.FindInDir(filepath.Join("./data/theme", entry.Name()))
		if !ok {
			continue
		}
		theme, err := loadThemeConfig(themePath)
		if err != nil || theme.Short != entry.Name() || !isValidThemeShort(theme.Short) {
			continue
		}
		themes = append(themes, theme)
	}
	sort.Slice(themes, func(i, j int) bool {
		priority := func(short string) int {
			if short == public.DefaultTheme {
				return 0
			}
			return 1
		}
		left, right := priority(themes[i].Short), priority(themes[j].Short)
		if left != right {
			return left < right
		}
		return themes[i].Short < themes[j].Short
	})
	return themes, nil
}

func themeDeletionFallback(themes []models.Theme, target string) (bool, string) {
	found := false
	fallback := ""
	for _, theme := range themes {
		if theme.Short == target {
			found = true
			continue
		}
		if fallback == "" {
			fallback = theme.Short
		}
	}
	return found, fallback
}

// ListThemes 列出所有主题
func ListThemes(c *gin.Context) {
	themes, err := installedThemes()
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取主题目录失败: "+err.Error())
		return
	}
	api.RespondSuccess(c, themes)
}

// DeleteTheme 删除主题
func DeleteTheme(c *gin.Context) {
	var req struct {
		Short string `json:"short" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	// 校验主题短名称，防止路径穿越（如 ../）导致删除工作目录外的任意文件
	if !isValidThemeShort(req.Short) {
		api.RespondError(c, http.StatusBadRequest, "无效的主题名称")
		return
	}

	themeMutationMu.Lock()
	defer themeMutationMu.Unlock()

	themes, err := installedThemes()
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取主题目录失败: "+err.Error())
		return
	}
	found, fallback := themeDeletionFallback(themes, req.Short)
	if !found {
		api.RespondError(c, http.StatusNotFound, "主题不存在")
		return
	}
	if fallback == "" {
		api.RespondError(c, http.StatusConflict, "至少需要保留一个可用主题")
		return
	}

	currentTheme, _ := config.GetAs[string](config.ThemeKey, public.DefaultTheme)
	if err := deleteInstalledTheme(dbcore.GetDBInstance(), req.Short, currentTheme, fallback); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "删除主题失败: "+err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题删除成功", gin.H{"theme": fallback})
}

func deleteInstalledTheme(db *gorm.DB, short, currentTheme, fallback string) error {
	themeDir := filepath.Join("./data/theme", short)
	tombstone, err := os.MkdirTemp("./data/theme", ".deleted-"+short+"-")
	if err != nil {
		return fmt.Errorf("prepare theme tombstone: %w", err)
	}
	_ = os.Remove(tombstone)
	if err := os.Rename(themeDir, tombstone); err != nil {
		return fmt.Errorf("move theme to tombstone: %w", err)
	}
	dbErr := db.Transaction(func(tx *gorm.DB) error {
		if currentTheme == short {
			value, err := json.Marshal(fallback)
			if err != nil {
				return err
			}
			item := config.ConfigItem{Key: config.ThemeKey, Value: string(value)}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}).Create(&item).Error; err != nil {
				return fmt.Errorf("switch to fallback theme: %w", err)
			}
		}
		if err := tx.Where("short = ?", short).Delete(&models.ThemeConfiguration{}).Error; err != nil {
			return fmt.Errorf("delete theme configuration: %w", err)
		}
		return nil
	})
	if dbErr != nil {
		if restoreErr := os.Rename(tombstone, themeDir); restoreErr != nil {
			return errors.Join(dbErr, fmt.Errorf("restore theme directory after database rollback: %w", restoreErr))
		}
		return dbErr
	}
	if err := os.RemoveAll(tombstone); err != nil {
		logger.Errorf("theme", "Theme %s was deleted but its hidden tombstone could not be removed: %v", short, err)
	}
	return nil
}

// SetTheme 设置主题
func SetTheme(c *gin.Context) {
	themeName := c.Query("theme")
	if themeName == "" {
		api.RespondError(c, http.StatusBadRequest, "主题名称不能为空")
		return
	}

	if !isValidThemeShort(themeName) {
		api.RespondError(c, http.StatusBadRequest, "无效的主题名称")
		return
	}
	themeMutationMu.Lock()
	defer themeMutationMu.Unlock()
	if !public.IsLocalThemeUsable(themeName) {
		api.RespondError(c, http.StatusNotFound, "主题不存在或不可用")
		return
	}

	if err := config.Set("theme", themeName); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "更新主题设置失败: "+err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题设置成功", gin.H{"theme": themeName})
}

// extractAndValidateTheme 解压并验证主题
func extractAndValidateTheme(zipPath string) (models.Theme, error) {
	themeMutationMu.Lock()
	defer themeMutationMu.Unlock()

	var themeInfo models.Theme

	// 打开ZIP文件
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return themeInfo, fmt.Errorf("无法打开ZIP文件: %v", err)
	}
	defer r.Close()

	if err := validateThemeArchive(r.File); err != nil {
		return themeInfo, err
	}

	themeConfigFile := zipThemeManifest(r.File)
	if themeConfigFile == nil {
		return themeInfo, fmt.Errorf("%s", thememanifest.MissingMessage())
	}

	// 读取主题配置
	rc, err := themeConfigFile.Open()
	if err != nil {
		return themeInfo, fmt.Errorf("无法读取主题配置文件: %v", err)
	}
	defer rc.Close()

	configData, err := io.ReadAll(io.LimitReader(rc, maxThemeManifestSize+1))
	if err != nil {
		return themeInfo, fmt.Errorf("读取主题配置失败: %v", err)
	}
	if len(configData) > maxThemeManifestSize {
		return themeInfo, fmt.Errorf("主题配置文件超过 %d 字节限制", maxThemeManifestSize)
	}

	if err := json.Unmarshal(configData, &themeInfo); err != nil {
		return themeInfo, fmt.Errorf("主题配置格式错误: %v", err)
	}

	// 验证必填字段
	if !models.IsLocalizedText(themeInfo.Name) || themeInfo.Short == "" {
		return themeInfo, fmt.Errorf("主题配置缺少必填字段（name、short）")
	}

	// 验证Short字段格式（只允许字母、数字、下划线、连字符）
	if !isValidThemeShort(themeInfo.Short) {
		return themeInfo, fmt.Errorf("主题short字段格式无效，只允许字母、数字、下划线和连字符")
	}

	if err := themeInfo.ValidateConfiguration(); err != nil {
		return themeInfo, err
	}

	themesDir := "./data/theme"
	if err := os.MkdirAll(themesDir, 0755); err != nil {
		return themeInfo, fmt.Errorf("创建主题目录失败: %v", err)
	}
	stageDir, err := os.MkdirTemp(themesDir, ".stage-"+themeInfo.Short+"-")
	if err != nil {
		return themeInfo, fmt.Errorf("创建主题暂存目录失败: %v", err)
	}
	defer os.RemoveAll(stageDir)

	hasIndex := false
	for _, f := range r.File {
		cleanName := zipEntryCleanName(f.Name)
		if cleanName == "." || filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(os.PathSeparator)) {
			return themeInfo, fmt.Errorf("主题文件包含不安全路径: %s", f.Name)
		}
		targetPath := filepath.Join(stageDir, cleanName)
		if cleanName == filepath.Join("dist", "index.html") {
			hasIndex = true
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return themeInfo, fmt.Errorf("创建目录失败: %v", err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return themeInfo, fmt.Errorf("创建目录失败: %v", err)
		}

		// 解压文件
		rc, err := f.Open()
		if err != nil {
			return themeInfo, fmt.Errorf("打开压缩文件失败: %v", err)
		}

		outFile, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			rc.Close()
			return themeInfo, fmt.Errorf("创建文件失败: %v", err)
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return themeInfo, fmt.Errorf("解压文件失败: %v", err)
		}
	}
	if !hasIndex {
		return themeInfo, errors.New("主题缺少 dist/index.html")
	}

	themeDir := filepath.Join(themesDir, themeInfo.Short)
	backupDir, err := os.MkdirTemp(themesDir, ".previous-"+themeInfo.Short+"-")
	if err != nil {
		return themeInfo, fmt.Errorf("创建主题回滚目录失败: %v", err)
	}
	_ = os.Remove(backupDir)
	hadPrevious := false
	if _, err := os.Stat(themeDir); err == nil {
		if err := os.Rename(themeDir, backupDir); err != nil {
			return themeInfo, fmt.Errorf("暂存原有主题失败: %v", err)
		}
		hadPrevious = true
	}
	if err := os.Rename(stageDir, themeDir); err != nil {
		if hadPrevious {
			_ = os.Rename(backupDir, themeDir)
		}
		return themeInfo, fmt.Errorf("启用新主题失败: %v", err)
	}
	if hadPrevious {
		_ = os.RemoveAll(backupDir)
	}

	go func(short, preview string) {
		_ = public.EnsureThemePreviewCard(short, preview)
	}(themeInfo.Short, themeInfo.Preview)

	return themeInfo, nil
}

func validateThemeArchive(files []*zip.File) error {
	if len(files) > maxThemeArchiveFiles {
		return fmt.Errorf("主题压缩包文件数量超过 %d 个限制", maxThemeArchiveFiles)
	}
	var total uint64
	for _, file := range files {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > maxThemeFileSize {
			return fmt.Errorf("主题文件 %s 超过 %d 字节限制", file.Name, maxThemeFileSize)
		}
		total += file.UncompressedSize64
		if total > maxThemeExtractedSize {
			return fmt.Errorf("主题解压后总大小超过 %d 字节限制", maxThemeExtractedSize)
		}
	}
	return nil
}

// zipEntryCleanName treats Windows zip paths (dist\index.html) as POSIX
// paths so Linux Docker can install themes packed by Compress-Archive.
func zipEntryCleanName(name string) string {
	return filepath.Clean(filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
}

func zipThemeManifest(files []*zip.File) *zip.File {
	var primary, legacy *zip.File
	for _, file := range files {
		if file.FileInfo().IsDir() {
			continue
		}
		switch thememanifest.RootName(file.Name) {
		case thememanifest.File:
			primary = file
		case thememanifest.LegacyFile:
			legacy = file
		}
	}
	if primary != nil {
		return primary
	}
	return legacy
}

// loadThemeConfig 加载主题配置
func loadThemeConfig(configPath string) (models.Theme, error) {
	var themeInfo models.Theme

	data, err := os.ReadFile(configPath)
	if err != nil {
		return themeInfo, err
	}

	if err := json.Unmarshal(data, &themeInfo); err != nil {
		return themeInfo, err
	}

	return themeInfo, nil
}

// isValidThemeShort 验证主题short字段格式
func isValidThemeShort(short string) bool {
	if short == "" || short == "default" {
		return false
	}

	for _, r := range short {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}

	return true
}

func themeFetchError(err error) error {
	switch {
	case errors.Is(err, themehttp.ErrInvalidURL):
		return errors.New("无效的下载地址")
	case errors.Is(err, themehttp.ErrUnsupportedScheme):
		return errors.New("仅支持 HTTP 和 HTTPS 地址")
	case errors.Is(err, themehttp.ErrPrivateAddress):
		return errors.New("不允许访问私有或保留地址")
	case errors.Is(err, themehttp.ErrDNS):
		return errors.New("无法解析下载地址")
	case errors.Is(err, themehttp.ErrRedirect), errors.Is(err, themehttp.ErrTooManyRedirects):
		return errors.New("下载跳转被拒绝")
	case errors.Is(err, themehttp.ErrTimeout):
		return errors.New("下载超时")
	case errors.Is(err, themehttp.ErrHTTPStatus):
		return fmt.Errorf("下载失败: %v", err)
	case errors.Is(err, themehttp.ErrEmpty):
		return errors.New("下载的主题文件为空")
	case errors.Is(err, themehttp.ErrTooLarge):
		return fmt.Errorf("%w: 主题压缩包超过 %d 字节限制", errThemeArchiveTooLarge, themehttp.MaxArchive)
	case errors.Is(err, themehttp.ErrTempFile):
		return errors.New("保存临时文件失败")
	default:
		return err
	}
}

func downloadThemeArchive(rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	path, _, err := themehttp.DownloadFile(ctx, rawURL, themehttp.MaxArchive, "lite-theme")
	if err != nil {
		return "", themeFetchError(err)
	}
	return path, nil
}

func fetchThemeJSON(rawURL string, maxBytes int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	data, err := themehttp.DownloadBytes(ctx, rawURL, maxBytes)
	if err != nil {
		if errors.Is(err, themehttp.ErrTooLarge) {
			return nil, errors.New("响应超过大小限制")
		}
		return nil, themeFetchError(err)
	}
	return data, nil
}

// getGitHubReleaseDownloadURL 从GitHub API获取最新release的下载链接
// 该函数通过GitHub API获取指定仓库最新release的资源下载链接
// 参考API: https://api.github.com/repos/{owner}/{repo}/releases/latest
// 参数:
//   - owner: GitHub仓库所有者
//   - repo: GitHub仓库名称
//
// 返回:
//   - 最新release的第一个资源的下载链接
//   - 错误信息（如果有）
func getGitHubReleaseDownloadURL(owner, repo string) (string, error) {
	if owner == "" || repo == "" {
		return "", errors.New("GitHub仓库所有者和仓库名称不能为空")
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	data, err := fetchThemeJSON(apiURL, themehttp.MaxGitHubJSON)
	if err != nil {
		return "", fmt.Errorf("获取GitHub release信息失败: %v", err)
	}

	var releaseInfo struct {
		Assets []struct {
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &releaseInfo); err != nil {
		return "", errors.New("解析GitHub API响应失败")
	}

	// 检查是否有可下载的资源
	if len(releaseInfo.Assets) == 0 {
		return "", errors.New("GitHub release中没有可下载的资源")
	}

	// 返回第一个资源的下载链接
	// 相当于shell命令: curl -s https://api.github.com/repos/owner/repo/releases/latest | jq -r ".assets[0].browser_download_url"
	return releaseInfo.Assets[0].BrowserDownloadURL, nil
}

// isGitHubRepoURL 检查URL是否是GitHub仓库地址
// 支持的格式:
// - https://github.com/owner/repo
// - https://github.com/owner/repo.git
// - https://www.github.com/owner/repo
// - http://github.com/owner/repo
// 返回:
//   - 是否是GitHub仓库URL
//   - 仓库所有者
//   - 仓库名称
func isGitHubRepoURL(urlStr string) (bool, string, string) {
	if urlStr == "" {
		return false, "", ""
	}

	// 检查URL是否包含github.com
	if !strings.Contains(strings.ToLower(urlStr), "github.com") {
		return false, "", ""
	}

	// 解析URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false, "", ""
	}

	// 检查主机名是否是github.com或www.github.com
	hostname := strings.ToLower(parsedURL.Host)
	if hostname != "github.com" && hostname != "www.github.com" {
		return false, "", ""
	}

	// 解析路径部分，提取owner和repo
	// 路径格式应该是 /owner/repo 或 /owner/repo.git
	path := strings.TrimPrefix(parsedURL.Path, "/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		return false, "", ""
	}

	owner := parts[0]
	repo := parts[1]

	// 如果repo以.git结尾，去掉这个后缀
	repo = strings.TrimSuffix(repo, ".git")

	return true, owner, repo
}

// UpdateTheme 更新主题
// 支持四种更新方式：
// 1. 使用主题原有URL下载更新
// 2. 提供新的直接下载URL进行更新
// 3. 提供GitHub仓库信息，从最新release下载更新
// 4. 如果主题URL是GitHub仓库地址，自动获取最新release
func UpdateTheme(c *gin.Context) {
	var req struct {
		Short    string `json:"short" binding:"required"` // 主题短名称
		URL      string `json:"url"`                      // 新的URL地址（可选）
		GitOwner string `json:"git_owner"`                // GitHub仓库所有者（可选）
		GitRepo  string `json:"git_repo"`                 // GitHub仓库名称（可选）
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	// 校验主题短名称，防止路径穿越（如 ../）访问工作目录外的文件
	if !isValidThemeShort(req.Short) {
		api.RespondError(c, http.StatusBadRequest, "无效的主题名称")
		return
	}

	// 检查主题是否存在
	themeDir := filepath.Join("./data/theme", req.Short)
	themeConfigPath, ok := thememanifest.FindInDir(themeDir)
	if !ok {
		api.RespondError(c, http.StatusNotFound, "主题不存在")
		return
	}

	// 加载现有主题配置
	themeInfo, err := loadThemeConfig(themeConfigPath)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取主题配置失败: "+err.Error())
		return
	}

	// 方式1和方式4: 尝试从原始URL下载主题
	// 如果原始URL是GitHub仓库地址，则自动获取最新release
	var themeFile string
	defer func() {
		if themeFile != "" {
			_ = os.Remove(themeFile)
		}
	}()
	tryDownload := func(rawURL string) bool {
		path, err := downloadThemeArchive(rawURL)
		if err != nil {
			return false
		}
		if themeFile != "" {
			_ = os.Remove(themeFile)
		}
		themeFile = path
		return true
	}

	if themeInfo.URL != "" {
		isGitHub, owner, repo := isGitHubRepoURL(themeInfo.URL)
		if isGitHub {
			gitHubURL, err := getGitHubReleaseDownloadURL(owner, repo)
			if err == nil {
				_ = tryDownload(gitHubURL)
			}
		} else {
			_ = tryDownload(themeInfo.URL)
		}
	}

	if themeFile == "" {
		if req.GitOwner != "" && req.GitRepo != "" {
			gitHubURL, err := getGitHubReleaseDownloadURL(req.GitOwner, req.GitRepo)
			if err != nil {
				api.RespondError(c, http.StatusBadRequest, "从GitHub获取下载链接失败: "+err.Error())
				return
			}
			path, err := downloadThemeArchive(gitHubURL)
			if err != nil {
				api.RespondError(c, http.StatusBadRequest, "从GitHub下载主题失败: "+err.Error())
				return
			}
			themeFile = path
		} else if req.URL != "" {
			isGitHub, owner, repo := isGitHubRepoURL(req.URL)
			if isGitHub {
				gitHubURL, err := getGitHubReleaseDownloadURL(owner, repo)
				if err != nil {
					api.RespondError(c, http.StatusBadRequest, "从GitHub获取下载链接失败: "+err.Error())
					return
				}
				path, err := downloadThemeArchive(gitHubURL)
				if err != nil {
					api.RespondError(c, http.StatusBadRequest, "从GitHub下载主题失败: "+err.Error())
					return
				}
				themeFile = path
			} else {
				path, err := downloadThemeArchive(req.URL)
				if err != nil {
					api.RespondError(c, http.StatusBadRequest, "从新URL下载主题失败: "+err.Error())
					return
				}
				themeFile = path
			}
		}
	}

	if themeFile == "" {
		api.RespondError(c, http.StatusBadRequest, "无法下载主题，请提供有效的URL或GitHub仓库信息")
		return
	}

	downloadedTheme, err := peekThemeFromZip(themeFile)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if downloadedTheme.Short != req.Short {
		api.RespondError(c, http.StatusBadRequest, "更新包主题标识与当前主题不一致")
		return
	}
	updatedThemeInfo, err := extractAndValidateTheme(themeFile)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题更新成功", updatedThemeInfo)
}

// peekThemeFromZip 仅从ZIP文件中读取主题清单并解析主题信息
// 不执行解压安装，用于preview模式
func peekThemeFromZip(zipPath string) (models.Theme, error) {
	var themeInfo models.Theme

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return themeInfo, fmt.Errorf("无法打开ZIP文件: %v", err)
	}
	defer r.Close()

	if err := validateThemeArchive(r.File); err != nil {
		return themeInfo, err
	}

	themeConfigFile := zipThemeManifest(r.File)
	if themeConfigFile == nil {
		return themeInfo, fmt.Errorf("%s，不是合法的主题包", thememanifest.MissingMessage())
	}

	rc, err := themeConfigFile.Open()
	if err != nil {
		return themeInfo, fmt.Errorf("无法读取主题配置文件: %v", err)
	}
	defer rc.Close()

	configData, err := io.ReadAll(io.LimitReader(rc, maxThemeManifestSize+1))
	if err != nil {
		return themeInfo, fmt.Errorf("读取主题配置失败: %v", err)
	}
	if len(configData) > maxThemeManifestSize {
		return themeInfo, fmt.Errorf("主题配置文件超过 %d 字节限制", maxThemeManifestSize)
	}

	if err := json.Unmarshal(configData, &themeInfo); err != nil {
		return themeInfo, fmt.Errorf("主题配置格式错误: %v", err)
	}

	if !models.IsLocalizedText(themeInfo.Name) || themeInfo.Short == "" {
		return themeInfo, fmt.Errorf("主题配置缺少必填字段（name、short）")
	}

	if !isValidThemeShort(themeInfo.Short) {
		return themeInfo, fmt.Errorf("主题short字段格式无效，只允许字母、数字、下划线和连字符")
	}

	if err := themeInfo.ValidateConfiguration(); err != nil {
		return themeInfo, err
	}

	return themeInfo, nil
}

// ImportTheme 导入远程主题
// 支持preview查询参数：preview=true时仅返回主题信息，否则下载安装
// 请求body: {"url": "https://..."}
// URL支持GitHub仓库地址（自动取latest release）和直接ZIP下载链接
func ImportTheme(c *gin.Context) {
	var req struct {
		URL string `json:"url" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	// 解析下载链接
	downloadURL := req.URL
	isGitHub, owner, repo := isGitHubRepoURL(req.URL)
	if isGitHub {
		gitHubURL, err := getGitHubReleaseDownloadURL(owner, repo)
		if err != nil {
			api.RespondError(c, http.StatusBadRequest, "从GitHub获取下载链接失败: "+err.Error())
			return
		}
		downloadURL = gitHubURL
	}

	tempFile, err := downloadThemeArchive(downloadURL)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "下载主题失败: "+err.Error())
		return
	}
	defer os.Remove(tempFile)

	// preview模式：仅解析并返回主题信息
	preview := c.Query("preview")
	if preview == "true" {
		themeInfo, err := peekThemeFromZip(tempFile)
		if err != nil {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}

		// 检查是否已存在同名主题
		exists := false
		themeDir := filepath.Join("./data/theme", themeInfo.Short)
		if _, err := os.Stat(themeDir); err == nil {
			exists = true
		}

		api.RespondSuccess(c, gin.H{
			"theme":  themeInfo,
			"exists": exists,
		})
		return
	}

	// 安装模式：检查是否存在同名主题
	// 先peek一下获取short名称用于检测冲突
	themeInfo, err := peekThemeFromZip(tempFile)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}

	overwritten := false
	themeDir := filepath.Join("./data/theme", themeInfo.Short)
	if _, err := os.Stat(themeDir); err == nil {
		overwritten = true
	}

	// 解压安装
	installedTheme, err := extractAndValidateTheme(tempFile)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}

	msg := "主题导入成功"
	if overwritten {
		msg = "主题导入成功（已覆盖同名主题）"
	}

	api.RespondSuccessMessage(c, msg, installedTheme)
}

func UpdateThemeSettings(c *gin.Context) {
	theme := c.Query("theme")
	if theme == "" {
		api.RespondError(c, http.StatusBadRequest, "主题名称不能为空")
		return
	}

	var req map[string]any

	err := c.ShouldBindJSON(&req)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	db := dbcore.GetDBInstance()

	data, err := json.Marshal(&req)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "生成主题配置失败: "+err.Error())
		return
	}

	var themeCfg models.ThemeConfiguration
	db.Where("short = ?", theme).
		Assign(models.ThemeConfiguration{Short: theme, Data: string(data)}).
		FirstOrCreate(&themeCfg)
	api.RespondSuccess(c, nil)
}
