package notification

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/komari-monitor/komari/database/models"
)

func TestValidateTrafficReportNotificationsRejectsEnabledWithoutCadence(t *testing.T) {
	err := ValidateTrafficReportNotifications([]models.TrafficReportNotification{{
		Client:         "client-a",
		Enable:         true,
		IncludeTraffic: true,
	}})

	assert.Error(t, err)
}

func TestValidateTrafficReportNotificationsRejectsEnabledWithoutContent(t *testing.T) {
	err := ValidateTrafficReportNotifications([]models.TrafficReportNotification{{
		Client: "client-a",
		Enable: true,
		Daily:  true,
	}})

	assert.Error(t, err)
}

func TestBuildEnabledTrafficReportNotificationsRequiresExistingCadence(t *testing.T) {
	_, err := buildEnabledTrafficReportNotifications([]string{"client-a"}, nil)
	assert.Error(t, err)

	_, err = buildEnabledTrafficReportNotifications([]string{"client-a"}, []models.TrafficReportNotification{{Client: "client-a"}})
	assert.Error(t, err)

	notifications, err := buildEnabledTrafficReportNotifications([]string{"client-a"}, []models.TrafficReportNotification{{
		Client:         "client-a",
		Daily:          true,
		IncludeTraffic: true,
	}})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	assert.Equal(t, "client-a", notifications[0].Client)
	assert.True(t, notifications[0].Enable)
}

func TestRequiredTrafficReportRetentionDays(t *testing.T) {
	tests := []struct {
		name          string
		notifications []models.TrafficReportNotification
		want          int
	}{
		{name: "none", want: 0},
		{
			name: "disabled reports do not retain data",
			notifications: []models.TrafficReportNotification{{
				Enable:  false,
				Monthly: true,
			}},
			want: 0,
		},
		{
			name: "daily",
			notifications: []models.TrafficReportNotification{{
				Enable: true,
				Daily:  true,
			}},
			want: 2,
		},
		{
			name: "weekly overrides daily",
			notifications: []models.TrafficReportNotification{
				{Enable: true, Daily: true},
				{Enable: true, Weekly: true},
			},
			want: 8,
		},
		{
			name: "monthly overrides shorter cadences",
			notifications: []models.TrafficReportNotification{{
				Enable:  true,
				Daily:   true,
				Weekly:  true,
				Monthly: true,
			}},
			want: 35,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, RequiredTrafficReportRetentionDays(test.notifications))
		})
	}
}
