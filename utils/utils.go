package utils

import (
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
)

func AverageReport(uuid string, time time.Time, records []common.Report) models.Record {
	var sumCPU, sumGPU, sumLOAD, sumTEMP float32
	var sumRAM, sumRAMTotal, sumSWAP, sumSWAPTotal, sumDISK, sumDISKTotal int64
	var sumNETIn, sumNETOut, sumNETTotalUp, sumNETTotalDown int64
	var sumPROCESS, sumConnections, sumConnectionsUDP int
	count := len(records)
	if count == 0 {
		return models.Record{}
	}
	for _, r := range records {
		sumCPU += float32(r.CPU.Usage)
		sumGPU += 0
		sumLOAD += float32(r.Load.Load1)
		sumTEMP += 0
		sumRAM += r.Ram.Used
		sumSWAP += r.Swap.Used
		sumDISK += r.Disk.Used
		sumNETIn += r.Network.Down
		sumNETOut += r.Network.Up
		sumNETTotalUp += r.Network.TotalUp
		sumNETTotalDown += r.Network.TotalDown
		sumPROCESS += r.Process
		sumConnections += r.Connections.TCP
		sumConnectionsUDP += r.Connections.UDP
	}

	// Create new aggregated record
	newRecord := models.Record{
		Client:         uuid,
		Time:           models.FromTime(time),
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
	return newRecord
}
