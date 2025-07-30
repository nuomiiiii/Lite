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
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/cmd/flags"
)

// 只有一个备份恢复操作在进行
var restoreMutex sync.Mutex

// UploadBackup 用于接收上传的备份文件并将其内容恢复到原始位置
func UploadBackup(c *gin.Context) {
	// 尝试获取锁，如果已有恢复操作在进行，则立即返回错误
	if !restoreMutex.TryLock() {
		api.RespondError(c, http.StatusConflict, "Another restore operation is already in progress")
		return
	}
	defer restoreMutex.Unlock()

	// 获取上传的文件
	file, header, err := c.Request.FormFile("backup")
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, fmt.Sprintf("Error getting uploaded file: %v", err))
		return
	}
	defer file.Close()

	// 检查文件是否为zip格式
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		api.RespondError(c, http.StatusBadRequest, "Uploaded file must be a ZIP archive")
		return
	}

	// 创建临时文件保存上传的zip
	tempFile, err := os.CreateTemp("", "backup-*.zip")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating temporary file: %v", err))
		return
	}
	tempFilePath := tempFile.Name()
	defer os.Remove(tempFilePath) // 确保临时文件最终被删除

	// 将上传的文件内容复制到临时文件
	_, err = io.Copy(tempFile, file)
	if err != nil {
		tempFile.Close()
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error saving uploaded file: %v", err))
		return
	}
	tempFile.Close() // 关闭文件以便后续操作

	// 打开zip文件准备解压
	zipReader, err := zip.OpenReader(tempFilePath)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error opening zip file: %v", err))
		return
	}
	defer zipReader.Close()

	// 检查是否包含备份标记文件
	hasMarkupFile := false
	for _, zipFile := range zipReader.File {
		if zipFile.Name == "komari-backup-markup" {
			hasMarkupFile = true
			break
		}
	}
	if !hasMarkupFile {
		api.RespondError(c, http.StatusBadRequest, "Invalid backup file: missing komari-backup-markup file")
		return
	}

	// 确保data目录存在
	if err := os.MkdirAll("./data", 0755); err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating data directory: %v", err))
		return
	}

	// 获取数据库文件名
	dbFileName := filepath.Base(flags.DatabaseFile)

	// 解压文件
	for _, zipFile := range zipReader.File {
		// 检查文件路径是否安全（防止路径遍历攻击）
		filePath := zipFile.Name
		if strings.Contains(filePath, "..") {
			log.Printf("Potentially unsafe path in zip: %s, skipping", filePath)
			continue
		}

		// 跳过备份标记文件
		if filePath == "komari-backup-markup" {
			continue
		}

		// 确定目标路径
		var destPath string
		if filePath == dbFileName {
			// 如果是数据库文件，恢复到数据库文件路径
			destPath = flags.DatabaseFile
		} else {
			// 其他文件恢复到data目录
			destPath = filepath.Join("./data", filePath)
		}

		// 如果是目录，创建目录
		if zipFile.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0755); err != nil {
				log.Printf("Error creating directory %s: %v", destPath, err)
			}
			continue
		}

		// 确保目标文件的目录存在
		destDir := filepath.Dir(destPath)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			log.Printf("Error creating directory %s: %v", destDir, err)
			continue
		}

		// 打开zip中的文件
		srcFile, err := zipFile.Open()
		if err != nil {
			log.Printf("Error opening file from zip %s: %v", filePath, err)
			continue
		}

		// 创建目标文件
		destFile, err := os.Create(destPath)
		if err != nil {
			srcFile.Close()
			log.Printf("Error creating file %s: %v", destPath, err)
			continue
		}

		// 复制内容
		_, err = io.Copy(destFile, srcFile)
		srcFile.Close()
		destFile.Close()
		if err != nil {
			log.Printf("Error extracting file %s: %v", destPath, err)
			continue
		}

		// 保持原始文件的修改时间
		if err := os.Chtimes(destPath, zipFile.Modified, zipFile.Modified); err != nil {
			log.Printf("Error setting file time for %s: %v", destPath, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Backup restored successfully. The service will restart shortly.",
	})

	go func() {
		log.Println("Backup restored, restarting service in 2 seconds...")
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()
}
