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
	if clientInfo.TrafficResetDay == nil {
		return nil, nil
	}
	return &v2.ConfigParams{MonthRotate: *clientInfo.TrafficResetDay}, nil
}
