package admin

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/rpc"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/backup"
	"github.com/nuomiiiii/lite/web/upload"
)

var (
	scheduleAdminRestart = func(delay time.Duration, task func()) { time.AfterFunc(delay, task) }
	exitAdminProcess     = os.Exit
)

func NewArchiveUploadHandler() *upload.Handler {
	return newArchiveUploadHandler(upload.DefaultStore)
}

func newArchiveUploadHandler(store *upload.Store) *upload.Handler {
	return upload.NewHandler(
		store,
		adminUploadOwner,
		map[upload.Purpose]upload.Finalizer{
			upload.PurposeBackup: finalizeBackupUpload,
			upload.PurposeTheme:  finalizeThemeUpload,
		},
		map[upload.Purpose]int64{
			upload.PurposeBackup: backup.MaxArchiveSize,
			upload.PurposeTheme:  maxThemeArchiveSize,
		},
	)
}

func adminUploadOwner(c *gin.Context) string {
	principal := api.GetPrincipal(c)
	if principal == nil {
		return "admin:unknown"
	}
	switch principal.Type {
	case rpc.PrincipalUser:
		return "admin:user:" + principal.UserUUID
	case rpc.PrincipalAPIKey:
		return "admin:api-key"
	default:
		return "admin:unknown"
	}
}

func finalizeBackupUpload(session upload.Session) (upload.Result, error) {
	restoreLock, err := backup.AcquireRestoreLock()
	if err != nil {
		return upload.Result{}, err
	}
	archive, err := os.Open(session.ArchivePath)
	if err != nil {
		restoreLock.Release()
		return upload.Result{}, fmt.Errorf("open merged backup: %w", err)
	}
	if err := restoreLock.SaveUploadedBackup(archive, session.Metadata.Filename); err != nil {
		_ = archive.Close()
		restoreLock.Release()
		return upload.Result{}, err
	}
	if err := archive.Close(); err != nil {
		restoreLock.Release()
		return upload.Result{}, fmt.Errorf("close merged backup: %w", err)
	}

	scheduleAdminRestart(2*time.Second, func() {
		logger.InfoArgs("admin-api", "Backup uploaded, restarting service in 2 seconds to apply on startup...")
		exitAdminProcess(0)
		// os.Exit never returns. This release is reached only by test doubles or
		// a custom exit hook that declined to terminate the process.
		restoreLock.Release()
	})
	return upload.Result{
		Message: "Backup uploaded successfully. The service will restart and apply the backup.",
		Data:    gin.H{"path": filepath.Join(".", "data", "backup.zip")},
	}, nil
}

func finalizeThemeUpload(session upload.Session) (upload.Result, error) {
	info, err := extractAndValidateTheme(session.ArchivePath)
	if err != nil {
		return upload.Result{}, err
	}
	return upload.Result{Message: "主题上传成功", Data: info}, nil
}
