package clients

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"

	"gorm.io/gorm"
)

// Report 表示客户端报告数据
// SaveReport 保存报告数据
func SaveReport(uuid string, data map[string]interface{}) (err error) {

	report, err := ParseReport(data)
	if err != nil {
		return err
	}
	err = SaveClientReport(uuid, report)
	if err != nil {

		return err
	}
	return nil

}

func GetClientUUIDByToken(token string) (clientUUID string, err error) {
	db := dbcore.GetDBInstance()
	var client models.Client
	err = db.Where("token = ?", token).First(&client).Error
	if err != nil {
		return "", err
	}
	return client.UUID, nil
}

func ParseReport(data map[string]interface{}) (report common.Report, err error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return common.Report{}, err
	}
	err = json.Unmarshal(jsonData, &report)
	if err != nil {
		return common.Report{}, err
	}
	return report, nil
}

// SaveClientReport 保存客户端报告到 Record 表
func SaveClientReport(clientUUID string, report common.Report) (err error) {
	db := dbcore.GetDBInstance()

	Record := models.Record{
		Client:         clientUUID,
		Time:           models.FromTime(time.Now()),
		Cpu:            float32(report.CPU.Usage),
		Gpu:            0, // Report 未提供 GPU Usage，设为 0（与原 nil 行为类似）
		Ram:            report.Ram.Used,
		RamTotal:       report.Ram.Total,
		Swap:           report.Swap.Used,
		SwapTotal:      report.Swap.Total,
		Load:           float32(report.Load.Load1), // 使用 Load1 作为主要负载指标
		Temp:           0,                          // Report 未提供 TEMP，设为 0（与原 nil 行为类似）
		Disk:           report.Disk.Used,
		DiskTotal:      report.Disk.Total,
		NetIn:          report.Network.Down,
		NetOut:         report.Network.Up,
		NetTotalUp:     report.Network.TotalUp,
		NetTotalDown:   report.Network.TotalDown,
		Process:        report.Process,
		Connections:    report.Connections.TCP,
		ConnectionsUdp: report.Connections.UDP,
	}

	// 使用事务确保 Record 和 ClientsInfo 一致性
	err = db.Transaction(func(tx *gorm.DB) error {
		// 保存 Record
		if err := tx.Create(&Record).Error; err != nil {
			return fmt.Errorf("failed to save Record: %v", err)
		}
		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

/*
// getString 从 map 中获取字符串
func getString(data map[string]interface{}, key string) string {
	if val, ok := data[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

// getInt 从 map 中获取整数
func getInt(data map[string]interface{}, key string) int {
	if val, ok := data[key]; ok {
		if num, ok := val.(float64); ok {
			return int(num)
		}
	}
	return 0
}

// getInt64 从 map 中获取 int64
func getInt64(data map[string]interface{}, key string) int64 {
	if val, ok := data[key]; ok {
		if num, ok := val.(float64); ok {
			return int64(num)
		}
	}
	return 0
}

// getFloat 从 map 中获取 float64
func getFloat(data map[string]interface{}, key string) float64 {
	if val, ok := data[key]; ok {
		if num, ok := val.(float64); ok {
			return num
		}
	}
	return 0.0
}
*/
