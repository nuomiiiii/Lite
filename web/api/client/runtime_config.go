package client

import (
	"github.com/komari-monitor/komari/database/clients"
	v2 "github.com/komari-monitor/komari/protocol/v2"
)

func getClientRuntimeConfig(uuid string) (*v2.ConfigParams, error) {
	clientInfo, err := clients.GetClientByUUID(uuid)
	if err != nil {
		return nil, err
	}
	profile, saved, err := clients.GetDeploymentProfile(uuid)
	if err != nil {
		return nil, err
	}
	if saved {
		config := profile.RuntimeConfig()
		return &config, nil
	}
	if clientInfo.TrafficResetDay == nil {
		return nil, nil
	}
	monthRotate := *clientInfo.TrafficResetDay
	return &v2.ConfigParams{MonthRotate: &monthRotate}, nil
}
