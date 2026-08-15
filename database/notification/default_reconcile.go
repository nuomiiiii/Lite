package notification

import (
	"context"

	"github.com/komari-monitor/komari/database/notificationdefaults"
)

func init() {
	notificationdefaults.RegisterTrafficReportRetentionReconciler(func() error {
		return EnsureTrafficReportMetricRetention(context.Background())
	})
}
