package notifier

import (
	"github.com/nuomiiiii/lite/database/models"
)

func checkMetricThreshold(records []models.Record, task models.LoadNotification, client *models.Client) bool {
	active, _, _ := evaluateMetricThreshold(records, task, client)
	return active
}
