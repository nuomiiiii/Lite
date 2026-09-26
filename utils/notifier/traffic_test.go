package notifier

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
)

func intPointer(value int) *int {
	return &value
}

func TestNextTrafficReminderPercentUsesConfiguredStep(t *testing.T) {
	mark, reached := nextTrafficReminderPercent(79.9, 80, 5)
	assert.False(t, reached)
	assert.Equal(t, 0, mark)

	mark, reached = nextTrafficReminderPercent(80, 80, 5)
	assert.True(t, reached)
	assert.Equal(t, 80, mark)

	mark, reached = nextTrafficReminderPercent(84.9, 80, 5)
	assert.Equal(t, 80, mark)

	mark, reached = nextTrafficReminderPercent(85, 80, 5)
	assert.Equal(t, 85, mark)

	mark, reached = nextTrafficReminderPercent(87, 80, 8)
	assert.Equal(t, 80, mark)
	mark, reached = nextTrafficReminderPercent(88, 80, 8)
	assert.Equal(t, 88, mark)

	mark, reached = nextTrafficReminderPercent(89, 80, 10)
	assert.Equal(t, 80, mark)
	mark, reached = nextTrafficReminderPercent(90, 80, 10)
	assert.Equal(t, 90, mark)

	mark, reached = nextTrafficReminderPercent(100, 80, 8)
	assert.Equal(t, 100, mark)

	mark, reached = nextTrafficReminderPercent(96, 80, 8)
	assert.Equal(t, 96, mark)

	// The historical 5-point ladder from 80 stays on the same marks.
	for _, pct := range []float64{80, 84, 85, 89, 90, 94, 95, 99, 100} {
		mark, reached = nextTrafficReminderPercent(pct, 80, 5)
		assert.True(t, reached)
		switch {
		case pct >= 100:
			assert.Equal(t, 100, mark)
		case pct >= 95:
			assert.Equal(t, 95, mark)
		case pct >= 90:
			assert.Equal(t, 90, mark)
		case pct >= 85:
			assert.Equal(t, 85, mark)
		default:
			assert.Equal(t, 80, mark)
		}
	}
}

func TestCurrentTrafficUsageIncludesCurrentCycleResetAllowance(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)
	client := models.Client{
		TrafficLimit:          100 * gib,
		TrafficLimitType:      "sum",
		TrafficResetDay:       intPointer(26),
		TrafficResetAllowance: 50 * gib,
		TrafficResetCycle:     "2026-07-26",
	}

	usage := currentTrafficUsage(
		client,
		70*gib,
		50*gib,
		time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
	)

	assert.Equal(t, int64(120*gib), usage.Used)
	assert.Equal(t, int64(150*gib), usage.Limit)
	assert.Equal(t, "sum", usage.Type)
	assert.Equal(t, 80.0, float64(usage.Used)/float64(usage.Limit)*100)
}

func TestCurrentTrafficUsageDropsExpiredResetAllowance(t *testing.T) {
	client := models.Client{
		TrafficLimit:          100,
		TrafficLimitType:      "max",
		TrafficResetDay:       intPointer(26),
		TrafficResetAllowance: 50,
		TrafficResetCycle:     "2026-07-26",
	}

	usage := currentTrafficUsage(
		client,
		40,
		60,
		time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC),
	)

	assert.Equal(t, int64(60), usage.Used)
	assert.Equal(t, int64(100), usage.Limit)
	assert.Equal(t, "max", usage.Type)
}

func TestCurrentTrafficUsageUsesConfiguredCountingMethod(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		want     int64
	}{
		{name: "sum", typeName: "sum", want: 100},
		{name: "maximum", typeName: "max", want: 70},
		{name: "minimum", typeName: "min", want: 30},
		{name: "upload", typeName: "up", want: 30},
		{name: "download", typeName: "down", want: 70},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := currentTrafficUsage(
				models.Client{TrafficLimit: 200, TrafficLimitType: tt.typeName},
				30,
				70,
				time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
			)
			assert.Equal(t, tt.want, usage.Used)
			assert.Equal(t, int64(200), usage.Limit)
			assert.Equal(t, tt.typeName, usage.Type)
		})
	}
}
