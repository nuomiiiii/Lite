package notification

import (
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
)

func TestValidatePingLossNotification(t *testing.T) {
	valid := models.PingLossNotification{
		Client:          "client-a",
		TaskId:          1,
		WindowSeconds:   60,
		LossThreshold:   5,
		MinimumSamples:  1,
		CooldownSeconds: 300,
	}
	assert.NoError(t, ValidatePingLossNotification(valid))

	tests := []struct {
		name   string
		mutate func(*models.PingLossNotification)
	}{
		{name: "missing client", mutate: func(n *models.PingLossNotification) { n.Client = "" }},
		{name: "missing task", mutate: func(n *models.PingLossNotification) { n.TaskId = 0 }},
		{name: "short window", mutate: func(n *models.PingLossNotification) { n.WindowSeconds = 59 }},
		{name: "invalid threshold", mutate: func(n *models.PingLossNotification) { n.LossThreshold = 0 }},
		{name: "invalid samples", mutate: func(n *models.PingLossNotification) { n.MinimumSamples = 0 }},
		{name: "short cooldown", mutate: func(n *models.PingLossNotification) { n.CooldownSeconds = 59 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			assert.Error(t, ValidatePingLossNotification(candidate))
		})
	}
}
