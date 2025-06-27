package records

import (
	"log"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func RecordOne(rec models.Record) error {
	db := dbcore.GetDBInstance()
	return db.Create(&rec).Error
}

func DeleteAll() error {
	db := dbcore.GetDBInstance()
	if err := db.Exec("DELETE FROM records_long_term").Error; err != nil {
		return err
	}
	return db.Exec("DELETE FROM records").Error
}

func GetLatestRecord(uuid string) (Record []models.Record, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("client = ?", uuid).Order("time DESC").Limit(1).Find(&Record).Error
	return
}

func DeleteRecordBefore(before time.Time) error {
	db := dbcore.GetDBInstance()
	db.Table("records_long_term").Where("time < ?", before).Delete(&models.Record{})
	return db.Where("time < ?", before).Delete(&models.Record{}).Error
}

func GetRecordsByClientAndTime(uuid string, start, end time.Time) ([]models.Record, error) {
	db := dbcore.GetDBInstance()
	var records []models.Record

	fourHoursAgo := time.Now().Add(-4*time.Hour - time.Minute)

	var recentRecords []models.Record
	recentStart := start
	if end.After(fourHoursAgo) {
		if recentStart.Before(fourHoursAgo) {
			recentStart = fourHoursAgo
		}
		err := db.Where("client = ? AND time >= ? AND time <= ?", uuid, recentStart, end).Order("time ASC").Find(&recentRecords).Error
		if err != nil {
			log.Printf("Error fetching recent records for client %s between %s and %s: %v", uuid, recentStart, end, err)
			return nil, err
		}
	}

	var long_term []models.Record
	err := db.Table("records_long_term").Where("client = ? AND time >= ? AND time <= ?", uuid, start, end).Order("time ASC").Find(&long_term).Error
	if err != nil {
		log.Printf("Error fetching long-term records for client %s between %s and %s: %v", uuid, start, end, err)
		return recentRecords, nil
	}

	if len(long_term) == 0 {
		// 没有查到long_term，返回全部recentRecords
		records = append(records, recentRecords...)
		return records, nil
	}

	// 查到了long_term，recentRecords按15分钟分组，每组只保留一条（取最新一条）
	grouped := make(map[string]models.Record)
	for _, rec := range recentRecords {
		key := rec.Time.ToTime().Truncate(15 * time.Minute).Format(time.RFC3339)
		if old, ok := grouped[key]; !ok || rec.Time.ToTime().After(old.Time.ToTime()) {
			grouped[key] = rec
		}
	}
	var groupedList []models.Record
	for _, rec := range grouped {
		groupedList = append(groupedList, rec)
	}
	sort.Slice(groupedList, func(i, j int) bool {
		return groupedList[i].Time.ToTime().Before(groupedList[j].Time.ToTime())
	})
	records = append(records, groupedList...)
	records = append(records, long_term...)
	return records, nil
}

func GetAllRecords() ([]models.Record, error) {
	db := dbcore.GetDBInstance()
	var records []models.Record
	var long_term []models.Record
	err := db.Table("records").Order("time ASC").Find(&records).Error
	if err != nil {
		log.Printf("Error fetching all records: %v", err)
		return nil, err
	}
	err = db.Table("records_long_term").Order("time ASC").Find(&long_term).Error
	if err != nil {
		log.Printf("Error fetching long-term records: %v", err)
		return records, nil
	}
	records = append(records, long_term...)
	return records, nil
}

// 压缩数据库
func CompactRecord() error {
	db := dbcore.GetDBInstance()
	err := migrateOldRecords(db)
	if err != nil {
		log.Printf("Error migrating old records: %v", err)
		return err
	}

	if flags.DatabaseType == "sqlite" {
		if err := db.Exec("VACUUM").Error; err != nil {
			log.Printf("Error vacuuming database: %v", err)
		}
	}
	//log.Printf("Record compaction completed")
	return nil
}

func migrateOldRecords(db *gorm.DB) error {
	// 计算 4 小时前的时间
	fourHoursAgo := time.Now().Add(-4 * time.Hour)

	// 查询 records 表中超过 4 小时的记录
	var records []models.Record
	if err := db.Table("records").Where("time < ?", fourHoursAgo).Find(&records).Error; err != nil {
		return err
	}

	if len(records) == 0 {
		return nil
	}

	// 按 Client 和 15 分钟时间段分组，并存储所有记录以计算分位数
	type groupData struct {
		Cpu            []float32
		Gpu            []float32
		Load           []float32
		Temp           []float32
		Ram            []int64
		RamTotal       []int64
		Swap           []int64
		SwapTotal      []int64
		Disk           []int64
		DiskTotal      []int64
		NetIn          []int64
		NetOut         []int64
		NetTotalUp     []int64
		NetTotalDown   []int64
		Process        []int
		Connections    []int
		ConnectionsUdp []int
	}

	groupedRecords := make(map[string]*groupData)
	for _, record := range records {
		key := record.Client + "_" + record.Time.ToTime().Truncate(15*time.Minute).Format(time.RFC3339)
		if _, ok := groupedRecords[key]; !ok {
			groupedRecords[key] = &groupData{}
		}
		data := groupedRecords[key]
		data.Cpu = append(data.Cpu, record.Cpu)
		data.Gpu = append(data.Gpu, record.Gpu)
		data.Load = append(data.Load, record.Load)
		data.Temp = append(data.Temp, record.Temp)
		data.Ram = append(data.Ram, record.Ram)
		data.RamTotal = append(data.RamTotal, record.RamTotal)
		data.Swap = append(data.Swap, record.Swap)
		data.SwapTotal = append(data.SwapTotal, record.SwapTotal)
		data.Disk = append(data.Disk, record.Disk)
		data.DiskTotal = append(data.DiskTotal, record.DiskTotal)
		data.NetIn = append(data.NetIn, record.NetIn)
		data.NetOut = append(data.NetOut, record.NetOut)
		data.NetTotalUp = append(data.NetTotalUp, record.NetTotalUp)
		data.NetTotalDown = append(data.NetTotalDown, record.NetTotalDown)
		data.Process = append(data.Process, record.Process)
		data.Connections = append(data.Connections, record.Connections)
		data.ConnectionsUdp = append(data.ConnectionsUdp, record.ConnectionsUdp)
	}

	getPercentile := func(values []float64, percentile float64) float64 {
		if len(values) == 0 {
			return 0
		}
		sortedValues := make([]float64, len(values))
		copy(sortedValues, values)
		sort.Float64s(sortedValues)
		index := float64(len(sortedValues)-1) * percentile
		lowerIndex := int(index)
		if lowerIndex >= len(sortedValues)-1 {
			return sortedValues[len(sortedValues)-1]
		}
		frac := index - float64(lowerIndex)
		return sortedValues[lowerIndex] + frac*(sortedValues[lowerIndex+1]-sortedValues[lowerIndex])
	}

	getIntPercentile := func(values []int64, percentile float64) int64 {
		if len(values) == 0 {
			return 0
		}
		floats := make([]float64, len(values))
		for i, v := range values {
			floats[i] = float64(v)
		}
		return int64(getPercentile(floats, percentile))
	}

	getInt32Percentile := func(values []int, percentile float64) int {
		if len(values) == 0 {
			return 0
		}
		floats := make([]float64, len(values))
		for i, v := range values {
			floats[i] = float64(v)
		}
		return int(getPercentile(floats, percentile))
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for key, data := range groupedRecords {
			// 解析 Client 和时间
			parts := strings.Split(key, "_")
			clientUUID := parts[0]
			timeSlot, err := time.Parse(time.RFC3339, strings.Join(parts[1:], "_"))
			if err != nil {
				return err
			}

			cpuFloats := make([]float64, len(data.Cpu))
			for i, v := range data.Cpu {
				cpuFloats[i] = float64(v)
			}
			gpuFloats := make([]float64, len(data.Gpu))
			for i, v := range data.Gpu {
				gpuFloats[i] = float64(v)
			}
			loadFloats := make([]float64, len(data.Load))
			for i, v := range data.Load {
				loadFloats[i] = float64(v)
			}
			tempFloats := make([]float64, len(data.Temp))
			for i, v := range data.Temp {
				tempFloats[i] = float64(v)
			}
			// 取高位
			high_percentile := 0.7
			// 检查 records_long_term 表中是否已存在相同的记录
			var existingCount int64
			if err := tx.Table("records_long_term").Where("client = ? AND time = ?", clientUUID, timeSlot).Count(&existingCount).Error; err != nil {
				return err
			}

			newRec := models.Record{
				Client:         clientUUID,
				Time:           models.FromTime(timeSlot),
				Cpu:            float32(getPercentile(cpuFloats, high_percentile)),
				Gpu:            float32(getPercentile(gpuFloats, high_percentile)),
				Load:           float32(getPercentile(loadFloats, high_percentile)),
				Temp:           float32(getPercentile(tempFloats, high_percentile)),
				Ram:            getIntPercentile(data.Ram, high_percentile),
				RamTotal:       getIntPercentile(data.RamTotal, high_percentile),
				Swap:           getIntPercentile(data.Swap, high_percentile),
				SwapTotal:      getIntPercentile(data.SwapTotal, high_percentile),
				Disk:           getIntPercentile(data.Disk, high_percentile),
				DiskTotal:      getIntPercentile(data.DiskTotal, high_percentile),
				NetIn:          getIntPercentile(data.NetIn, 0.2),
				NetOut:         getIntPercentile(data.NetOut, 0.2),
				NetTotalUp:     getIntPercentile(data.NetTotalUp, high_percentile),
				NetTotalDown:   getIntPercentile(data.NetTotalDown, high_percentile),
				Process:        getInt32Percentile(data.Process, high_percentile),
				Connections:    getInt32Percentile(data.Connections, high_percentile),
				ConnectionsUdp: getInt32Percentile(data.ConnectionsUdp, high_percentile),
			}

			// 如果记录已存在则更新，否则创建新记录
			if existingCount > 0 {
				if err := tx.Table("records_long_term").Where("client = ? AND time = ?", clientUUID, timeSlot).Updates(&newRec).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Table("records_long_term").Create(&newRec).Error; err != nil {
					return err
				}
			}
		}

		// 删除 records 表中的旧数据
		if err := tx.Table("records").Where("time < ?", fourHoursAgo.Add(-1*time.Hour)).Delete(&models.Record{}).Error; err != nil {
			return err
		}

		return nil
	})
}
