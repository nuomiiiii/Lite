package tasks

import (
	"strconv"
	"strings"

	"github.com/nuomiiiii/lite/database/models"
)

func classifyReturnRoute(path models.StringArray) (string, float64) {
	hops := make([]returnRouteSignature, 0, len(path))
	for _, value := range path {
		asn, _ := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(value), "AS"))
		if asn > 0 {
			hops = append(hops, returnRouteSignature{asn: asn})
		}
	}
	return classifyReturnRouteSignatures(hops)
}

func classifyReturnRouteHops(ips []string, asns map[string]int) (string, float64) {
	hops := make([]returnRouteSignature, 0, len(ips))
	for _, value := range ips {
		ip := strings.TrimSpace(value)
		if ip != "" {
			hops = append(hops, returnRouteSignature{ip: ip, asn: asns[ip]})
		}
	}
	return classifyReturnRouteSignatures(hops)
}

func classifyReturnRouteSignatures(hops []returnRouteSignature) (string, float64) {
	return classifyReturnRouteSignaturesWithRules(hops, currentReturnRouteRules())
}
