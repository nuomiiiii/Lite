package utils

import (
	"sort"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
)

// AverageReport 根据 topPercentage 参数计算报告的平均值。
// 如果 topPercentage 为 0，则计算所有记录的平均值。
// 如果 topPercentage 大于 0 且小于等于 1，则计算从大到小排序后的前 topPercentage 记录的平均值。
func AverageReport(uuid string, time time.Time, records []common.Report, topPercentage float64) models.Record {
	count := len(records)
	if count == 0 {
		return models.Record{}
	}

	recordsToAverageCount := count
	if topPercentage > 0 && topPercentage <= 1 {
		recordsToAverageCount = int(float64(count) * topPercentage)
		if recordsToAverageCount == 0 && count > 0 {
			recordsToAverageCount = 1 // 确保至少选择一个记录，除非总数为0
		}
	}

	// 定义一个辅助函数，用于排序和求和
	sumAndSort := func(getFloat32Value func(common.Report) float32, getInt64Value func(common.Report) int64, isFloat bool) (float32, int64) {
		if isFloat {
			sort.Slice(records, func(i, j int) bool {
				return getFloat32Value(records[i]) > getFloat32Value(records[j])
			})
			var sum float32
			for i := 0; i < recordsToAverageCount; i++ {
				sum += getFloat32Value(records[i])
			}
			return sum, 0
		} else {
			sort.Slice(records, func(i, j int) bool {
				return getInt64Value(records[i]) > getInt64Value(records[j])
			})
			var sum int64
			for i := 0; i < recordsToAverageCount; i++ {
				sum += getInt64Value(records[i])
			}
			return 0, sum
		}
	}

	var sumCPU, sumLOAD float32
	var sumRAM, sumRAMTotal, sumSWAP, sumSWAPTotal, sumDISK, sumDISKTotal, sumNETIn, sumNETOut, sumNETTotalUp, sumNETTotalDown int64
	var sumPROCESS, sumConnections, sumConnectionsUDP int

	if topPercentage > 0 && topPercentage <= 1 {
		sumCPU, _ = sumAndSort(func(r common.Report) float32 { return float32(r.CPU.Usage) }, nil, true)
		sumLOAD, _ = sumAndSort(func(r common.Report) float32 { return float32(r.Load.Load1) }, nil, true)

		_, sumRAM = sumAndSort(nil, func(r common.Report) int64 { return r.Ram.Used }, false)
		//_, sumRAMTotal = sumAndSort(nil, nil, func(r common.Report) int64 { return r.Ram.Total }, false)
		_, sumSWAP = sumAndSort(nil, func(r common.Report) int64 { return r.Swap.Used }, false)
		//_, sumSWAPTotal = sumAndSort(nil, nil, func(r common.Report) int64 { return r.Swap.Total }, false)
		_, sumDISK = sumAndSort(nil, func(r common.Report) int64 { return r.Disk.Used }, false)
		//_, sumDISKTotal = sumAndSort(nil, nil, func(r common.Report) int64 { return r.Disk.Total }, false)
		_, sumNETIn = sumAndSort(nil, func(r common.Report) int64 { return r.Network.Down }, false)
		_, sumNETOut = sumAndSort(nil, func(r common.Report) int64 { return r.Network.Up }, false)
		_, sumNETTotalUp = sumAndSort(nil, func(r common.Report) int64 { return r.Network.TotalUp }, false)
		_, sumNETTotalDown = sumAndSort(nil, func(r common.Report) int64 { return r.Network.TotalDown }, false)

		_, sumInt := sumAndSort(nil, func(r common.Report) int64 { return int64(r.Process) }, false)
		sumPROCESS = int(sumInt)
		_, sumInt = sumAndSort(nil, func(r common.Report) int64 { return int64(r.Connections.TCP) }, false)
		sumConnections = int(sumInt)
		_, sumInt = sumAndSort(nil, func(r common.Report) int64 { return int64(r.Connections.UDP) }, false)
		sumConnectionsUDP = int(sumInt)

	} else { // 计算所有记录的平均值
		for _, r := range records {
			sumCPU += float32(r.CPU.Usage)
			sumLOAD += float32(r.Load.Load1)
			sumRAM += r.Ram.Used
			sumRAMTotal += r.Ram.Total
			sumSWAP += r.Swap.Used
			sumSWAPTotal += r.Swap.Total
			sumDISK += r.Disk.Used
			sumDISKTotal += r.Disk.Total
			sumNETIn += r.Network.Down
			sumNETOut += r.Network.Up
			sumNETTotalUp += r.Network.TotalUp
			sumNETTotalDown += r.Network.TotalDown
			sumPROCESS += r.Process
			sumConnections += r.Connections.TCP
			sumConnectionsUDP += r.Connections.UDP
		}
	}

	// 创建新的聚合记录
	newRecord := models.Record{
		Client:         uuid,
		Time:           models.FromTime(time),
		Cpu:            sumCPU / float32(recordsToAverageCount),
		Gpu:            0, // 保持原始行为
		Ram:            sumRAM / int64(recordsToAverageCount),
		RamTotal:       records[0].Ram.Total,
		Swap:           sumSWAP / int64(recordsToAverageCount),
		SwapTotal:      records[0].Swap.Total,
		Load:           sumLOAD / float32(recordsToAverageCount),
		Temp:           0, // 保持原始行为
		Disk:           sumDISK / int64(recordsToAverageCount),
		DiskTotal:      records[0].Disk.Total,
		NetIn:          sumNETIn / int64(recordsToAverageCount),
		NetOut:         sumNETOut / int64(recordsToAverageCount),
		NetTotalUp:     sumNETTotalUp / int64(recordsToAverageCount),
		NetTotalDown:   sumNETTotalDown / int64(recordsToAverageCount),
		Process:        sumPROCESS / recordsToAverageCount,
		Connections:    sumConnections / recordsToAverageCount,
		ConnectionsUdp: sumConnectionsUDP / recordsToAverageCount,
	}
	return newRecord
}
