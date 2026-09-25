package notifier

import (
	"github.com/nuomiiiii/lite/database/trafficledger"
)

func sumTrafficDeltas(records []trafficDeltaRecord, previous *trafficDeltaRecord) (int64, int64) {
	return trafficledger.SumTrafficDeltas(records, previous)
}

func trafficDeltaOrFallback(storedDelta int64, storedDeltaSet bool, currentTotal, previousTotal int64) int64 {
	return trafficledger.TrafficDeltaOrFallback(storedDelta, storedDeltaSet, currentTotal, previousTotal)
}
