package admin

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/cmd/flags"
)

// DownloadBackup 用于打包 ./data 目录及数据库文件为 zip 并通过 HTTP 下载
func DownloadBackup(c *gin.Context) {
	backupFileName := fmt.Sprintf("backup-%s.zip", time.Now().Format("20060102-150405"))
	c.Writer.Header().Set("Content-Type", "application/zip")
	c.Writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", backupFileName))

	zipWriter := zip.NewWriter(c.Writer)
	defer zipWriter.Close()

	err := filepath.Walk("./data", func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 获取在 zip 包内的相对路径
		relPath, err := filepath.Rel("./data", filePath)
		if err != nil {
			return err
		}

		// 跳过根目录本身，只打包内容
		if relPath == "." {
			return nil
		}

		// zip 内路径统一用正斜杠
		zipPath := filepath.ToSlash(relPath)

		if info.IsDir() {
			// 在 zip 中创建目录项
			_, err = zipWriter.CreateHeader(&zip.FileHeader{
				Name:     zipPath + "/",
				Method:   zip.Deflate, //  zip.Store 不压缩
				Modified: info.ModTime(),
			})
			return err
		}

		// 普通文件，写入 zip
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer file.Close()

		writer, err := zipWriter.CreateHeader(&zip.FileHeader{
			Name:     zipPath,
			Method:   zip.Deflate,
			Modified: info.ModTime(),
		})
		if err != nil {
			return err
		}

		_, err = io.Copy(writer, file)
		return err
	})

	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error archiving data folder: %v", err))
		return
	}

	// 获取 ./data 和数据库文件的绝对路径以进行可靠的比较
	dataAbsPath, err := filepath.Abs("./data")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error getting absolute path for data directory: %v", err))
		return
	}

	dbFilePath := flags.DatabaseFile
	dbAbsPath, err := filepath.Abs(dbFilePath)
	if err != nil {
		// 如果无法解析数据库路径，可能是一个配置问题，但我们记录并跳过，避免备份失败
		log.Printf("Could not determine absolute path for database file '%s', skipping explicit addition. Error: %v\n", dbFilePath, err)
		return
	}

	// 如果数据库文件的绝对路径不是以 ./data 目录的绝对路径开头，则单独添加它
	if !strings.HasPrefix(dbAbsPath, dataAbsPath) {
		dbFileName := filepath.Base(dbFilePath)
		dbInfo, err := os.Stat(dbFilePath)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("Database file '%s' does not exist, skipping.\n", dbFilePath)
				return // 数据库不存在是正常情况，直接返回
			}
			api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error stating database file: %v", err))
			return
		}

		if !dbInfo.IsDir() {
			file, err := os.Open(dbFilePath)
			if err != nil {
				api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error opening database file: %v", err))
				return
			}
			defer file.Close()

			writer, err := zipWriter.CreateHeader(&zip.FileHeader{
				Name:     dbFileName,
				Method:   zip.Deflate,
				Modified: dbInfo.ModTime(),
			})
			if err != nil {
				api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating zip entry for database file: %v", err))
				return
			}

			_, err = io.Copy(writer, file)
			if err != nil {
				api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error writing database file to zip: %v", err))
				return
			}
		}
	} else {
		log.Printf("Database file '%s' is within './data', skipping explicit addition.\n", dbFilePath)
	}

	// 添加备份标记文件
	markupContent := "此文件为 Komari 备份标记文件，请勿删除。\nThis is a Komari backup markup file, please do not delete.\n\n备份时间 / Backup Time: " + time.Now().Format("2006-01-02 15:04:05")
	markupWriter, err := zipWriter.CreateHeader(&zip.FileHeader{
		Name:     "komari-backup-markup",
		Method:   zip.Deflate,
		Modified: time.Now(),
	})
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating backup markup file: %v", err))
		return
	}

	_, err = markupWriter.Write([]byte(markupContent))
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error writing backup markup file: %v", err))
		return
	}

	// zipWriter.Close() 由 defer 自动调用
}
