// Package managedconfig implements shared managed-theme configuration rules.
package managedconfig

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/nuomiiiii/lite/database/models"
	logger "github.com/nuomiiiii/lite/utils/log"
	"gorm.io/gorm"
)

const (
	TypeNodes     = "nodes"
	TypePingTasks = "pingtasks"
)

func DefaultValue(item models.ManagedThemeConfigurationItem) any {
	value := item.Default
	if item.Type == "select" && (value == nil || value == "") && item.Options != "" {
		options := strings.Split(item.Options, ",")
		if len(options) > 0 {
			return strings.TrimSpace(options[0])
		}
	}
	if value != nil {
		if item.Type == TypeNodes {
			return NodeIDs(value)
		}
		if item.Type == TypePingTasks {
			return PingTaskIDs(value)
		}
		return value
	}
	switch item.Type {
	case "number":
		return 0
	case "switch":
		return false
	case TypeNodes:
		return []string{}
	case TypePingTasks:
		return []uint{}
	default:
		return ""
	}
}

// ResolveForOutput converts selectors to arrays and drops references deleted
// from the database before configuration is exposed to public themes.
func ResolveForOutput(db *gorm.DB, values map[string]any, items []models.ManagedThemeConfigurationItem) error {
	hasNodes, hasPingTasks := false, false
	for _, item := range items {
		hasNodes = hasNodes || item.Type == TypeNodes
		hasPingTasks = hasPingTasks || item.Type == TypePingTasks
	}

	liveNodes := map[string]struct{}{}
	if hasNodes {
		var nodes []models.Client
		if err := db.Select("uuid").Find(&nodes).Error; err != nil {
			return err
		}
		for _, node := range nodes {
			liveNodes[node.UUID] = struct{}{}
		}
	}
	liveTasks := map[uint]struct{}{}
	if hasPingTasks {
		var tasks []models.PingTask
		if err := db.Select("id").Find(&tasks).Error; err != nil {
			return err
		}
		for _, task := range tasks {
			liveTasks[task.Id] = struct{}{}
		}
	}

	for _, item := range items {
		if item.Key == "" {
			continue
		}
		switch item.Type {
		case TypeNodes:
			selected := NodeIDs(values[item.Key])
			filtered := make([]string, 0, len(selected))
			for _, id := range selected {
				if _, ok := liveNodes[id]; ok {
					filtered = append(filtered, id)
				}
			}
			values[item.Key] = filtered
		case TypePingTasks:
			selected := PingTaskIDs(values[item.Key])
			filtered := make([]uint, 0, len(selected))
			for _, id := range selected {
				if _, ok := liveTasks[id]; ok {
					filtered = append(filtered, id)
				}
			}
			values[item.Key] = filtered
		}
	}
	return nil
}

func NodeIDs(value any) []string {
	var ids []string
	if err := decodeSelector(value, &ids); err != nil {
		logger.Errorf("theme-config", "Ignoring invalid nodes selector: %v", err)
		return []string{}
	}
	return uniqueStrings(ids)
}

func PingTaskIDs(value any) []uint {
	var ids []uint
	if err := decodeSelector(value, &ids); err != nil {
		logger.Errorf("theme-config", "Ignoring invalid pingtasks selector: %v", err)
		return []uint{}
	}
	return uniqueUint(ids)
}

func decodeSelector(value any, target any) error {
	if value == nil {
		return nil
	}
	var data []byte
	var err error
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		data = []byte(text)
	} else {
		data, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", reflect.TypeOf(target), err)
	}
	return nil
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueUint(values []uint) []uint {
	result := make([]uint, 0, len(values))
	seen := map[uint]struct{}{}
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
