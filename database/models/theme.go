package models

// Theme represents a komari theme information
type Theme struct {
	Name        string `json:"name"`        // 主题名称
	Short       string `json:"short"`       // 短名称，用作文件夹名
	Description string `json:"description"` // 主题描述
	Version     string `json:"version"`     // 版本号
	Author      string `json:"author"`      // 作者
	URL         string `json:"url"`         // 主题URL
	Preview     string `json:"preview"`     // 预览图片相对路径
}
