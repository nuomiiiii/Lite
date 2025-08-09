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
	"github.com/komari-monitor/komari/database/dbcore"
)

// copyDatabaseToTemp 安全地复制数据库文件到临时目录
// 对于SQLite数据库，会使用BACKUP命令确保数据一致性
func copyDatabaseToTemp(dbFilePath, tempDir string) (string, error) {
	if dbFilePath == "" {
		return "", fmt.Errorf("database file path is empty")
	}

	// 检查数据库文件是否存在
	if _, err := os.Stat(dbFilePath); os.IsNotExist(err) {
		return "", fmt.Errorf("database file does not exist: %s", dbFilePath)
	}

	dbFileName := filepath.Base(dbFilePath)
	tempDbPath := filepath.Join(tempDir, dbFileName)

	// 如果是SQLite数据库，使用SQLite的BACKUP命令来确保一致性
	if flags.DatabaseType == "sqlite" || flags.DatabaseType == "" {
		db := dbcore.GetDBInstance()

		// 获取底层的SQLite连接
		sqlDB, err := db.DB()
		if err != nil {
			return "", fmt.Errorf("failed to get underlying database connection: %v", err)
		}

		// 使用SQLite的BACKUP API来创建一致的备份
		backupSQL := fmt.Sprintf("BACKUP DATABASE main TO '%s'", tempDbPath)
		_, err = sqlDB.Exec(backupSQL)
		if err != nil {
			// 如果BACKUP命令失败，回退到文件复制方法
			log.Printf("SQLite BACKUP command failed, falling back to file copy: %v", err)
			return copyFileToTemp(dbFilePath, tempDbPath)
		}

		log.Printf("Database backed up using SQLite BACKUP to: %s", tempDbPath)
		return tempDbPath, nil
	} else {
		// 对于非SQLite数据库，直接复制文件
		return copyFileToTemp(dbFilePath, tempDbPath)
	}
}

// copyFileToTemp 简单的文件复制到临时目录
func copyFileToTemp(srcPath, destPath string) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("failed to open source file: %v", err)
	}
	defer src.Close()

	dest, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create destination file: %v", err)
	}
	defer dest.Close()

	_, err = io.Copy(dest, src)
	if err != nil {
		return "", fmt.Errorf("failed to copy file: %v", err)
	}

	log.Printf("Database file copied to: %s", destPath)
	return destPath, nil
}

// DownloadBackup 用于打包 ./data 目录及数据库文件为 zip 并通过 HTTP 下载
func DownloadBackup(c *gin.Context) {
	backupFileName := fmt.Sprintf("backup-%d.zip", time.Now().UnixMicro())
	c.Writer.Header().Set("Content-Type", "application/zip")
	c.Writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", backupFileName))

	zipWriter := zip.NewWriter(c.Writer)
	defer zipWriter.Close()

	// 创建临时目录用于存放数据库备份
	tempDir, err := os.MkdirTemp("", "komari-backup-*")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating temporary directory: %v", err))
		return
	}
	defer os.RemoveAll(tempDir) // 清理临时目录

	err = filepath.Walk("./data", func(filePath string, info os.FileInfo, err error) error {
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
		// 使用安全的数据库复制方法
		tempDbPath, err := copyDatabaseToTemp(dbFilePath, tempDir)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("Database file '%s' does not exist, skipping.\n", dbFilePath)
			} else {
				api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error copying database file: %v", err))
				return
			}
		} else {
			// 将临时数据库文件添加到zip
			dbInfo, err := os.Stat(tempDbPath)
			if err != nil {
				api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error stating temp database file: %v", err))
				return
			}

			file, err := os.Open(tempDbPath)
			if err != nil {
				api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error opening temp database file: %v", err))
				return
			}
			defer file.Close()

			writer, err := zipWriter.CreateHeader(&zip.FileHeader{
				Name:     filepath.Base(dbFilePath), // 使用原始数据库文件名
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
	markupContent := "此文件为 Komari 备份标记文件，请勿删除。\nThis is a Komari backup markup file, please do not delete.\n\n备份时间 / Backup Time: " + time.Now().Format(time.RFC3339)
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
