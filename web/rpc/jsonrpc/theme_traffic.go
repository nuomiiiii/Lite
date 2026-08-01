package jsonrpc

import "github.com/komari-monitor/komari/database/models"

// applyThemeTrafficCompatibility keeps existing themes on the current billing
// cycle quota while the admin API continues to expose the configured base quota.
func applyThemeTrafficCompatibility(nodes []models.Client) {
	for index := range nodes {
		node := &nodes[index]
		limit := node.EffectiveTrafficLimit
		if limit == 0 && node.TrafficLimit > 0 {
			limit = node.TrafficLimit
		}
		typeName := node.EffectiveTrafficType
		if typeName == "" {
			typeName = node.TrafficLimitType
		}
		node.TrafficLimit = limit
		node.TrafficLimitType = typeName
	}
}
