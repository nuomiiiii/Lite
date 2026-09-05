package jsonrpc

import (
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/models"
)

// themeNodeForbiddenJSONKeys are never serialized on common:getNodes,
// for anonymous or administrator callers.
var themeNodeForbiddenJSONKeys = []string{
	"token",
	"version",
	"remark",
	"deployment_status",
	"remote_protocol",
	"remote_control_enabled",
	"remote_control_protected",
	"traffic_reset_allowance",
	"traffic_reset_cycle",
	"created_at",
	"updated_at",
}

// ThemeNode is the public theme DTO for common:getNodes.
// It is a deliberate subset of models.Client and must not be replaced by
// serializing the full client record.
type ThemeNode struct {
	UUID                  string     `json:"uuid,omitempty"`
	Name                  string     `json:"name"`
	CpuName               string     `json:"cpu_name"`
	Virtualization        string     `json:"virtualization"`
	Arch                  string     `json:"arch"`
	CpuCores              int        `json:"cpu_cores"`
	CpuPhysicalCores      int        `json:"cpu_physical_cores"`
	OS                    string     `json:"os"`
	KernelVersion         string     `json:"kernel_version"`
	GpuName               string     `json:"gpu_name"`
	IPv4                  string     `json:"ipv4,omitempty"`
	IPv6                  string     `json:"ipv6,omitempty"`
	Region                string     `json:"region"`
	RegionOverride        string     `json:"region_override"`
	PublicRemark          string     `json:"public_remark,omitempty"`
	MemTotal              int64      `json:"mem_total"`
	SwapTotal             int64      `json:"swap_total"`
	DiskTotal             int64      `json:"disk_total"`
	Weight                int        `json:"weight"`
	Price                 float64    `json:"price"`
	BillingCycle          int        `json:"billing_cycle"`
	AutoRenewal           bool       `json:"auto_renewal"`
	Currency              string     `json:"currency"`
	ExpiredAt             *time.Time `json:"expired_at"`
	Group                 string     `json:"group"`
	Tags                  string     `json:"tags"`
	Bandwidth             string     `json:"bandwidth"`
	Hidden                bool       `json:"hidden"`
	TrafficLimit          int64      `json:"traffic_limit"`
	TrafficLimitType      string     `json:"traffic_limit_type"`
	TrafficResetDay       *int       `json:"traffic_reset_day,omitempty"`
	EffectiveTrafficLimit int64      `json:"effective_traffic_limit"`
	EffectiveTrafficType  string     `json:"effective_traffic_type"`
}

func toThemeNode(node models.Client) ThemeNode {
	return ThemeNode{
		UUID:                  node.UUID,
		Name:                  node.Name,
		CpuName:               node.CpuName,
		Virtualization:        node.Virtualization,
		Arch:                  node.Arch,
		CpuCores:              node.CpuCores,
		CpuPhysicalCores:      node.CpuPhysicalCores,
		OS:                    node.OS,
		KernelVersion:         node.KernelVersion,
		GpuName:               node.GpuName,
		IPv4:                  node.IPv4,
		IPv6:                  node.IPv6,
		Region:                node.Region,
		RegionOverride:        node.RegionOverride,
		PublicRemark:          node.PublicRemark,
		MemTotal:              node.MemTotal,
		SwapTotal:             node.SwapTotal,
		DiskTotal:             node.DiskTotal,
		Weight:                node.Weight,
		Price:                 node.Price,
		BillingCycle:          node.BillingCycle,
		AutoRenewal:           node.AutoRenewal,
		Currency:              node.Currency,
		ExpiredAt:             node.ExpiredAt,
		Group:                 node.Group,
		Tags:                  node.Tags,
		Bandwidth:             node.Bandwidth,
		Hidden:                node.Hidden,
		TrafficLimit:          node.TrafficLimit,
		TrafficLimitType:      node.TrafficLimitType,
		TrafficResetDay:       node.TrafficResetDay,
		EffectiveTrafficLimit: node.EffectiveTrafficLimit,
		EffectiveTrafficType:  node.EffectiveTrafficType,
	}
}

func redactGuestThemeClient(node models.Client, sendIPAddr bool) models.Client {
	if sendIPAddr {
		if node.IPv4 != "" {
			node.IPv4 = strings.Split(node.IPv4, ".")[0] + ".*.*.*"
		}
		if node.IPv6 != "" {
			node.IPv6 = strings.Split(node.IPv6, ":")[0] + ":*:*:*:*:*:*:*"
		}
	} else {
		node.IPv4 = ""
		node.IPv6 = ""
	}
	return node
}

func presentThemeNodes(nodes []models.Client, isAdmin, sendIPAddrToGuest bool) []ThemeNode {
	applyThemeTrafficCompatibility(nodes)
	out := make([]ThemeNode, 0, len(nodes))
	for _, node := range nodes {
		if !isAdmin && node.Hidden {
			continue
		}
		if !isAdmin {
			node = redactGuestThemeClient(node, sendIPAddrToGuest)
		}
		out = append(out, toThemeNode(node))
	}
	return out
}

func themeNodeByUUID(nodes []ThemeNode, uuid string) (ThemeNode, bool) {
	for _, node := range nodes {
		if node.UUID == uuid {
			return node, true
		}
	}
	return ThemeNode{}, false
}

func themeNodeMap(nodes []ThemeNode) map[string]ThemeNode {
	out := make(map[string]ThemeNode, len(nodes))
	for _, node := range nodes {
		out[node.UUID] = node
	}
	return out
}
