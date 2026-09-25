package notificationdefaults

import (
	"fmt"

	"gorm.io/gorm"
)

func ApplyPingLossDefaultsToTaskClients(db *gorm.DB, taskID uint, clients []string) error {
	if db == nil || taskID == 0 || len(clients) == 0 {
		return nil
	}
	cfg, err := GetPingLossNotificationDefaultConfig()
	if err != nil {
		return fmt.Errorf("load ping loss notification default: %w", err)
	}
	return ApplyLoadedPingLossDefaultsToTaskClients(db, cfg, taskID, clients)
}
