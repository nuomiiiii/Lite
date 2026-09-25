package trafficledger

import (
	"time"

	"github.com/nuomiiiii/lite/database/models"
)

func trafficCycleInclusiveEnd(start time.Time, resetDay int) time.Time {
	return NextCycleStart(start, resetDay).AddDate(0, 0, -1)
}

func NextCycleStart(start time.Time, resetDay int) time.Time {
	return scheduleForDay(resetDay).Next(start)
}

func CurrentTrafficCycle(resetDay *int, now time.Time) (time.Time, string, error) {
	return CurrentTrafficCycleFor(models.Client{TrafficResetDay: resetDay}, now)
}

func calibrationAppliesToCurrentCycle(resetDay *int, cycle string, now time.Time) bool {
	_, currentCycle, err := CurrentTrafficCycle(resetDay, now)
	return err == nil && cycle == currentCycle
}
