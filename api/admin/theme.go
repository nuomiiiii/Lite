package admin

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/models"
)

// UploadTheme 上传主题
func UploadTheme(c *gin.Context) {
	// 读取上传的文件内容
	data, err := io.ReadAll(c.Request.Body)
	if err != nil || len(data) == 0 {
		api.RespondError(c, http.StatusBadRequest, "请选择要上传的主题文件")
		return
	}

	// 临时文件名
	tempFile := filepath.Join(os.TempDir(), "uploaded_theme.zip")
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "保存文件失败: "+err.Error())
		return
	}
	defer os.Remove(tempFile)

	// 检查文件扩展名（这里假定上传的就是zip）
	if !strings.HasSuffix(strings.ToLower(tempFile), ".zip") {
		api.RespondError(c, http.StatusBadRequest, "只支持ZIP格式的主题文件")
		return
	}

	// 解压ZIP文件并验证
	themeInfo, err := extractAndValidateTheme(tempFile)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题上传成功", themeInfo)
}

// ListThemes 列出所有主题
func ListThemes(c *gin.Context) {
	dataDir := "./data/theme"

	// 确保主题目录存在
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		api.RespondSuccess(c, []models.Theme{})
		return
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取主题目录失败: "+err.Error())
		return
	}

	var themes []models.Theme
	for _, entry := range entries {
		if entry.IsDir() {
			themeConfigPath := filepath.Join(dataDir, entry.Name(), "komari-theme.json")
			if themeInfo, err := loadThemeConfig(themeConfigPath); err == nil {
				themes = append(themes, themeInfo)
			}
		}
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

	if req.Short == "default" {
		api.RespondError(c, http.StatusBadRequest, "默认主题不能删除")
		return
	}

	themeDir := filepath.Join("./data/theme", req.Short)

	// 检查主题是否存在
	if _, err := os.Stat(themeDir); os.IsNotExist(err) {
		api.RespondError(c, http.StatusNotFound, "主题不存在")
		return
	}

	// 删除主题目录
	if err := os.RemoveAll(themeDir); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "删除主题失败: "+err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题删除成功", nil)
}

// SetTheme 设置主题
func SetTheme(c *gin.Context) {
	themeName := c.Query("theme")
	if themeName == "" {
		api.RespondError(c, http.StatusBadRequest, "主题名称不能为空")
		return
	}

	// 如果不是default主题，检查主题是否存在
	if themeName != "default" {
		themeDir := filepath.Join("./data/theme", themeName)
		themeConfigPath := filepath.Join(themeDir, "komari-theme.json")

		if _, err := os.Stat(themeConfigPath); os.IsNotExist(err) {
			api.RespondError(c, http.StatusNotFound, "主题不存在")
			return
		}
	}

	// 更新配置
	updateData := map[string]interface{}{
		"theme": themeName,
	}

	if err := config.Update(updateData); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "更新主题设置失败: "+err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题设置成功", gin.H{"theme": themeName})
}

// extractAndValidateTheme 解压并验证主题
func extractAndValidateTheme(zipPath string) (models.Theme, error) {
	var themeInfo models.Theme

	// 打开ZIP文件
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return themeInfo, fmt.Errorf("无法打开ZIP文件: %v", err)
	}
	defer r.Close()

	// 查找komari-theme.json文件
	var themeConfigFile *zip.File
	for _, f := range r.File {
		if f.Name == "komari-theme.json" {
			themeConfigFile = f
			break
		}
	}

	if themeConfigFile == nil {
		return themeInfo, fmt.Errorf("主题配置文件 komari-theme.json 不存在")
	}

	// 读取主题配置
	rc, err := themeConfigFile.Open()
	if err != nil {
		return themeInfo, fmt.Errorf("无法读取主题配置文件: %v", err)
	}
	defer rc.Close()

	configData, err := io.ReadAll(rc)
	if err != nil {
		return themeInfo, fmt.Errorf("读取主题配置失败: %v", err)
	}

	if err := json.Unmarshal(configData, &themeInfo); err != nil {
		return themeInfo, fmt.Errorf("主题配置格式错误: %v", err)
	}

	// 验证必填字段
	if themeInfo.Name == "" || themeInfo.Short == "" {
		return themeInfo, fmt.Errorf("主题配置缺少必填字段（name、short）")
	}

	// 验证Short字段格式（只允许字母、数字、下划线、连字符）
	if !isValidThemeShort(themeInfo.Short) {
		return themeInfo, fmt.Errorf("主题short字段格式无效，只允许字母、数字、下划线和连字符")
	}

	// 创建主题目录
	themeDir := filepath.Join("./data/theme", themeInfo.Short)

	// 如果目录已存在，先删除
	if _, err := os.Stat(themeDir); err == nil {
		if err := os.RemoveAll(themeDir); err != nil {
			return themeInfo, fmt.Errorf("删除原有主题失败: %v", err)
		}
	}

	if err := os.MkdirAll(themeDir, 0755); err != nil {
		return themeInfo, fmt.Errorf("创建主题目录失败: %v", err)
	}

	// 解压文件到主题目录
	for _, f := range r.File {
		path := filepath.Join(themeDir, f.Name)

		// 安全检查，防止路径遍历攻击
		if !strings.HasPrefix(path, filepath.Clean(themeDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.FileInfo().Mode())
			continue
		}

		// 创建目录
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return themeInfo, fmt.Errorf("创建目录失败: %v", err)
		}

		// 解压文件
		rc, err := f.Open()
		if err != nil {
			return themeInfo, fmt.Errorf("打开压缩文件失败: %v", err)
		}

		outFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
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

	return themeInfo, nil
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

// downloadThemeFromURL 从URL下载主题文件
func downloadThemeFromURL(url string) ([]byte, error) {
	// 发送HTTP GET请求
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("下载主题文件失败: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载主题文件失败，HTTP状态码: %d", resp.StatusCode)
	}

	// 读取响应内容
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取主题文件内容失败: %v", err)
	}

	// 检查文件大小
	if len(data) == 0 {
		return nil, errors.New("下载的主题文件为空")
	}

	return data, nil
}

// UpdateTheme 更新主题
func UpdateTheme(c *gin.Context) {
	var req struct {
		Short string `json:"short" binding:"required"` // 主题短名称
		URL   string `json:"url"`                      // 新的URL地址（可选）
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	// 检查主题是否存在
	themeDir := filepath.Join("./data/theme", req.Short)
	themeConfigPath := filepath.Join(themeDir, "komari-theme.json")

	if _, err := os.Stat(themeConfigPath); os.IsNotExist(err) {
		api.RespondError(c, http.StatusNotFound, "主题不存在")
		return
	}

	// 加载现有主题配置
	themeInfo, err := loadThemeConfig(themeConfigPath)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取主题配置失败: "+err.Error())
		return
	}

	// 尝试从原始URL下载主题
	var themeData []byte
	var downloadURL string

	if themeInfo.URL != "" {
		themeData, err = downloadThemeFromURL(themeInfo.URL)
		if err == nil {
			downloadURL = themeInfo.URL
		}
	}

	// 如果原始URL下载失败且提供了新URL，则尝试从新URL下载
	if (themeData == nil || len(themeData) == 0) && req.URL != "" {
		themeData, err = downloadThemeFromURL(req.URL)
		if err != nil {
			api.RespondError(c, http.StatusBadRequest, "从新URL下载主题失败: "+err.Error())
			return
		}
		downloadURL = req.URL
	}

	// 如果没有成功下载主题数据
	if themeData == nil || len(themeData) == 0 {
		api.RespondError(c, http.StatusBadRequest, "无法从原始URL下载主题，请提供新的URL")
		return
	}

	// 临时文件名
	tempFile := filepath.Join(os.TempDir(), "downloaded_theme.zip")
	if err := os.WriteFile(tempFile, themeData, 0644); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "保存文件失败: "+err.Error())
		return
	}
	defer os.Remove(tempFile)

	// 解压ZIP文件并验证
	updatedThemeInfo, err := extractAndValidateTheme(tempFile)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}

	// 如果下载URL与原始URL不同，更新主题配置中的URL
	if downloadURL != themeInfo.URL {
		updatedThemeInfo.URL = downloadURL

		// 更新主题配置文件
		updatedConfigPath := filepath.Join("./data/theme", updatedThemeInfo.Short, "komari-theme.json")
		updatedConfigData, err := json.MarshalIndent(updatedThemeInfo, "", "  ")
		if err != nil {
			api.RespondError(c, http.StatusInternalServerError, "生成主题配置失败: "+err.Error())
			return
		}

		if err := os.WriteFile(updatedConfigPath, updatedConfigData, 0644); err != nil {
			api.RespondError(c, http.StatusInternalServerError, "更新主题配置文件失败: "+err.Error())
			return
		}
	}

	api.RespondSuccessMessage(c, "主题更新成功", updatedThemeInfo)
}
