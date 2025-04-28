package records

import (
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
	err = db.Where("ClientUUID = ?", uuid).Order("time DESC").Limit(1).Find(&Record).Error
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

// 压缩数据库，针对每个ClientUUID，3小时内的数据不动，24小时内的数据精简为每分钟一条，3天为每15分钟一条，7天为每1小时一条，30天为每12小时一条。所有精简的数据是取 [0.02,0.98] 区间内的平均值
func CompactRecord() error {
	db := dbcore.GetDBInstance()

	var clientUUIDs []string
	if err := db.Model(&models.Record{}).Distinct("ClientUUID").Pluck("ClientUUID", &clientUUIDs).Error; err != nil {
		return err
	}

	now := time.Now()
	threeHoursAgo := now.Add(-3 * time.Hour)
	oneDayAgo := now.Add(-24 * time.Hour)
	threeDaysAgo := now.Add(-3 * 24 * time.Hour)
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)

	for _, clientUUID := range clientUUIDs {
		// Process each time window
		if err := db.Transaction(func(tx *gorm.DB) error {
			// 24 hours to 3 hours: compact to 1-minute intervals
			if err := compactTimeWindow(tx, clientUUID, oneDayAgo, threeHoursAgo, time.Minute, func(recordss []models.Record) models.Record {
				return aggregateRecords(recordss, time.Minute)
			}); err != nil {
				return err
			}

			// 3 days to 24 hours: compact to 15-minute intervals
			if err := compactTimeWindow(tx, clientUUID, threeDaysAgo, oneDayAgo, 15*time.Minute, func(recordss []models.Record) models.Record {
				return aggregateRecords(recordss, 15*time.Minute)
			}); err != nil {
				return err
			}

			// 7 days to 3 days: compact to 1-hour intervals
			if err := compactTimeWindow(tx, clientUUID, sevenDaysAgo, threeDaysAgo, time.Hour, func(recordss []models.Record) models.Record {
				return aggregateRecords(recordss, time.Hour)
			}); err != nil {
				return err
			}

			// 30 days to 7 days: compact to 12-hour intervals
			if err := compactTimeWindow(tx, clientUUID, thirtyDaysAgo, sevenDaysAgo, 12*time.Hour, func(recordss []models.Record) models.Record {
				return aggregateRecords(recordss, 12*time.Hour)
			}); err != nil {
				return err
			}

			return nil
		}); err != nil {
			return err
		}
	}

	return nil
}

// compactTimeWindow compacts recordss within a specific time window
func compactTimeWindow(db *gorm.DB, clientUUID string, startTime, endTime time.Time, interval time.Duration, aggregator func([]models.Record) models.Record) error {
	var recordss []models.Record
	if err := db.Where("ClientUUID = ? AND Time >= ? AND Time < ?", clientUUID, startTime, endTime).
		Order("Time ASC").Find(&recordss).Error; err != nil {
		return err
	}

	if len(recordss) == 0 {
		return nil
	}

	// Group recordss by interval
	groups := make(map[int64][]models.Record)
	for _, records := range recordss {
		// Truncate time to the start of the interval
		intervalStart := records.Time.Truncate(interval).Unix()
		groups[intervalStart] = append(groups[intervalStart], records)
	}

	// Process each group
	for intervalStart, group := range groups {
		if len(group) <= 1 {
			continue // Skip groups with single records
		}

		// Aggregate recordss
		aggregated := aggregator(group)

		// Delete original recordss
		if err := db.Where("ClientUUID = ? AND Time >= ? AND Time < ?",
			clientUUID, time.Unix(intervalStart, 0), time.Unix(intervalStart, 0).Add(interval)).
			Delete(&models.Record{}).Error; err != nil {
			return err
		}

		// Insert aggregated records
		if err := db.Create(&aggregated).Error; err != nil {
			return err
		}
	}

	return nil
}

// aggregateRecords aggregates a group of recordss into a single records
func aggregateRecords(recordss []models.Record, interval time.Duration) models.Record {
	if len(recordss) == 0 {
		return models.Record{}
	}

	// Initialize result with first records's metadata
	result := models.Record{
		Client: recordss[0].Client,
		Time:   recordss[0].Time.Truncate(interval),
	}

	// Collect values for quantile mean calculation
	var cpuValues, gpuValues, loadValues, tempValues []float32
	var ram, ramTotal, swap, swapTotal, disk, diskTotal, netIn, netOut, netTotalUp, netTotalDown []int64
	var process, connections, connectionsUDP []int

	for _, r := range recordss {
		cpuValues = append(cpuValues, r.CPU)

		gpuValues = append(gpuValues, r.GPU)
		loadValues = append(loadValues, r.LOAD)
		tempValues = append(tempValues, r.TEMP)
		ram = append(ram, r.RAM)
		ramTotal = append(ramTotal, r.RAMTotal)
		swap = append(swap, r.SWAP)
		swapTotal = append(swapTotal, r.SWAPTotal)
		disk = append(disk, r.DISK)
		diskTotal = append(diskTotal, r.DISKTotal)
		netIn = append(netIn, r.NETIn)
		netOut = append(netOut, r.NETOut)
		netTotalUp = append(netTotalUp, r.NETTotalUp)
		netTotalDown = append(netTotalDown, r.NETTotalDown)
		process = append(process, r.PROCESS)
		connections = append(connections, r.Connections)
		connectionsUDP = append(connectionsUDP, r.ConnectionsUDP)
	}

	// Apply quantile mean for float32 fields
	result.CPU = QuantileMean(cpuValues)
	result.GPU = QuantileMean(gpuValues)
	result.LOAD = QuantileMean(loadValues)
	result.TEMP = QuantileMean(tempValues)

	// Simple mean for integer fields
	sumInt64 := func(values []int64) int64 {
		if len(values) == 0 {
			return 0
		}
		sum := int64(0)
		for _, v := range values {
			sum += v
		}
		return sum / int64(len(values))
	}

	sumInt := func(values []int) int {
		if len(values) == 0 {
			return 0
		}
		sum := 0
		for _, v := range values {
			sum += v
		}
		return sum / len(values)
	}

	result.RAM = sumInt64(ram)
	result.RAMTotal = sumInt64(ramTotal)
	result.SWAP = sumInt64(swap)
	result.SWAPTotal = sumInt64(swapTotal)
	result.DISK = sumInt64(disk)
	result.DISKTotal = sumInt64(diskTotal)
	result.NETIn = sumInt64(netIn)
	result.NETOut = sumInt64(netOut)
	result.NETTotalUp = sumInt64(netTotalUp)
	result.NETTotalDown = sumInt64(netTotalDown)
	result.PROCESS = sumInt(process)
	result.Connections = sumInt(connections)
	result.ConnectionsUDP = sumInt(connectionsUDP)

	return result
}
