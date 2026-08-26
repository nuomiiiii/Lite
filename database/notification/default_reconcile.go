package notification

import (
	"context"

	"github.com/nuomiiiii/lite/database/notificationdefaults"
)

func init() {
	notificationdefaults.RegisterTrafficReportRetentionReconciler(func() error {
		return EnsureTrafficReportMetricRetention(context.Background())
	})
}
