package records

import (
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func RecordOne(rec models.Record) error {
	db := dbcore.GetDBInstance()
	return db.Create(&rec).Error
}

func DeleteAll() error {
	db := dbcore.GetDBInstance()
	return db.Exec("DELETE FROM Record").Error
}

func GetLatestRecord(uuid string) (Record []models.Record, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("client = ?", uuid).Order("time DESC").Limit(1).Find(&Record).Error
	return
}

func DeleteRecordBefore(before time.Time) error {
	db := dbcore.GetDBInstance()
	return db.Where("time < ?", before).Delete(&models.Record{}).Error
}

// 计算区间 [0.02, 0.98] 的平均值
func QuantileMean(values []float32) float32 {
	if len(values) == 0 {
		return 0
	}
	// 排序数据
	sorted := make([]float32, len(values))
	copy(sorted, values)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	// 区间边缘index
	lower := int(0.02 * float64(len(sorted)))
	upper := int(0.98 * float64(len(sorted)))
	if lower >= upper || lower >= len(sorted) {
		return 0
	}

	sum := float32(0)
	count := 0
	for i := lower; i < upper && i < len(sorted); i++ {
		sum += sorted[i]
		count++
	}
	if count == 0 {
		return 0
	}
	return sum / float32(count)
}

// 压缩数据库，针对每个ClientUUID，10分钟内的数据不动，24小时内的数据精简为每15分钟一条，3天为每30分钟一条，7天为每1小时一条，30天为每12小时一条。所有精简的数据是取 [0.02,0.98] 区间内的平均值
func CompactRecord() error {
	db := dbcore.GetDBInstance()
	log.Printf("Compacting records...")
	var clientUUIDs []string
	if err := db.Model(&models.Record{}).Distinct("client").Pluck("client", &clientUUIDs).Error; err != nil {
		return err
	}

	now := time.Now()
	tenMinutesAgo := now.Add(-10 * time.Minute)
	oneDayAgo := now.Add(-24 * time.Hour)
	threeDaysAgo := now.Add(-3 * 24 * time.Hour)
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)

	for _, clientUUID := range clientUUIDs {
		// Process each time range with specific aggregation intervals
		if err := processTimeRange(db, clientUUID, thirtyDaysAgo, sevenDaysAgo, 12*time.Hour, now); err != nil {
			log.Printf("Error compacting 30d-7d for client %s: %v", clientUUID, err)
			continue
		}
		if err := processTimeRange(db, clientUUID, sevenDaysAgo, threeDaysAgo, 1*time.Hour, now); err != nil {
			log.Printf("Error compacting 7d-3d for client %s: %v", clientUUID, err)
			continue
		}
		if err := processTimeRange(db, clientUUID, threeDaysAgo, oneDayAgo, 30*time.Minute, now); err != nil {
			log.Printf("Error compacting 3d-1d for client %s: %v", clientUUID, err)
			continue
		}
		if err := processTimeRange(db, clientUUID, oneDayAgo, tenMinutesAgo, 15*time.Minute, now); err != nil {
			log.Printf("Error compacting 1d-10m for client %s: %v", clientUUID, err)
			continue
		}
	}

	if err := db.Exec("VACUUM").Error; err != nil {
		log.Printf("Error vacuuming the database: %v", err)
	}
	log.Printf("Record compaction completed")
	return nil
}

func processTimeRange(db *gorm.DB, clientUUID string, start, end time.Time, interval time.Duration, now time.Time) error {
	// Round start time to the nearest interval
	start = start.Truncate(interval)
	end = end.Truncate(interval)

	// Get all records in the time range
	var records []models.Record
	if err := db.Where("client = ? AND time >= ? AND time < ?", clientUUID, start, end).
		Order("time ASC").
		Find(&records).Error; err != nil {
		return err
	}

	if len(records) == 0 {
		return nil
	}

	// Group records by interval
	type aggregatedRecord struct {
		StartTime time.Time
		Records   []models.Record
	}
	var aggregatedRecords []aggregatedRecord
	currentGroup := aggregatedRecord{StartTime: start}
	currentGroup.Records = make([]models.Record, 0)

	for _, record := range records {
		groupTime := record.Time.Truncate(interval)
		if !groupTime.Equal(currentGroup.StartTime) {
			if len(currentGroup.Records) > 0 {
				aggregatedRecords = append(aggregatedRecords, currentGroup)
			}
			currentGroup = aggregatedRecord{StartTime: groupTime}
			currentGroup.Records = []models.Record{record}
		} else {
			currentGroup.Records = append(currentGroup.Records, record)
		}
	}
	if len(currentGroup.Records) > 0 {
		aggregatedRecords = append(aggregatedRecords, currentGroup)
	}

	// Begin transaction
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	// Delete original records in the time range
	if err := tx.Where("client = ? AND time >= ? AND time < ?", clientUUID, start, end).Delete(&models.Record{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	// Insert aggregated records
	for _, agg := range aggregatedRecords {
		if len(agg.Records) == 0 {
			continue
		}

		// Calculate averages and sums
		var sumCPU, sumGPU, sumLOAD, sumTEMP float32
		var sumRAM, sumRAMTotal, sumSWAP, sumSWAPTotal, sumDISK, sumDISKTotal int64
		var sumNETIn, sumNETOut, sumNETTotalUp, sumNETTotalDown int64
		var sumPROCESS, sumConnections, sumConnectionsUDP int
		count := len(agg.Records)

		for _, r := range agg.Records {
			sumCPU += r.Cpu
			sumGPU += r.Gpu
			sumLOAD += r.Load
			sumTEMP += r.Temp
			sumRAM += r.Ram
			sumRAMTotal += r.RamTotal
			sumSWAP += r.Swap
			sumSWAPTotal += r.SwapTotal
			sumDISK += r.Disk
			sumDISKTotal += r.DiskTotal
			sumNETIn += r.NetIn
			sumNETOut += r.NetOut
			sumNETTotalUp += r.NetTotalUp
			sumNETTotalDown += r.NetTotalDown
			sumPROCESS += r.Process
			sumConnections += r.Connections
			sumConnectionsUDP += r.ConnectionsUdp
		}

		// Create new aggregated record
		newRecord := models.Record{
			Client:         clientUUID,
			Time:           agg.StartTime,
			Cpu:            sumCPU / float32(count),
			Gpu:            sumGPU / float32(count),
			Ram:            sumRAM / int64(count),
			RamTotal:       sumRAMTotal / int64(count),
			Swap:           sumSWAP / int64(count),
			SwapTotal:      sumSWAPTotal / int64(count),
			Load:           sumLOAD / float32(count),
			Temp:           sumTEMP / float32(count),
			Disk:           sumDISK / int64(count),
			DiskTotal:      sumDISKTotal / int64(count),
			NetIn:          sumNETIn / int64(count),
			NetOut:         sumNETOut / int64(count),
			NetTotalUp:     sumNETTotalUp / int64(count),
			NetTotalDown:   sumNETTotalDown / int64(count),
			Process:        sumPROCESS / count,
			Connections:    sumConnections / count,
			ConnectionsUdp: sumConnectionsUDP / count,
		}

		if err := tx.Create(&newRecord).Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit().Error
}

func Save(rec models.Record) error {
	db := dbcore.GetDBInstance()
	return db.Create(&rec).Error
}
