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
		Time:           time,
		CPU:            sumCPU / float32(count),
		GPU:            sumGPU / float32(count),
		RAM:            sumRAM / int64(count),
		RAMTotal:       sumRAMTotal / int64(count),
		SWAP:           sumSWAP / int64(count),
		SWAPTotal:      sumSWAPTotal / int64(count),
		LOAD:           sumLOAD / float32(count),
		TEMP:           sumTEMP / float32(count),
		DISK:           sumDISK / int64(count),
		DISKTotal:      sumDISKTotal / int64(count),
		NETIn:          sumNETIn / int64(count),
		NETOut:         sumNETOut / int64(count),
		NETTotalUp:     sumNETTotalUp / int64(count),
		NETTotalDown:   sumNETTotalDown / int64(count),
		PROCESS:        sumPROCESS / count,
		Connections:    sumConnections / count,
		ConnectionsUDP: sumConnectionsUDP / count,
	}
	return newRecord
}
