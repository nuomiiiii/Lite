package jsonrpc

import (
	"context"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/stretchr/testify/require"
)

func TestAdminListClientsSummaryViewStripsTokensAndBulkyFields(t *testing.T) {
	prev := listAdminClients
	defer func() { listAdminClients = prev }()

	listAdminClients = func() ([]models.Client, error) {
		return []models.Client{
			{
				UUID:             "n-1",
				Token:            "secret-agent-token",
				Name:             "LAX-1",
				IPv4:             "203.0.113.10",
				CpuName:          "AMD EPYC",
				GpuName:          "RTX 4090",
				KernelVersion:    "6.1.0",
				Virtualization:   "kvm",
				Remark:           "private note",
				PublicRemark:     "very long public remark",
				DeploymentStatus: "applied",
			},
		}, nil
	}

	req := &rpc.JsonRpcRequest{
		Params: map[string]any{"view": "summary"},
	}
	res, rpcErr := adminListClients(context.Background(), req)
	require.Nil(t, rpcErr)

	rows, ok := res.([]models.Client)
	require.True(t, ok)
	require.Len(t, rows, 1)
	require.Equal(t, "", rows[0].Token, "summary view must never leak agent token")
	require.Equal(t, "LAX-1", rows[0].Name)
	require.Equal(t, "private note", rows[0].Remark)
	require.Equal(t, "applied", rows[0].DeploymentStatus)
	require.Equal(t, "", rows[0].PublicRemark)
	require.Equal(t, "", rows[0].CpuName)
	require.Equal(t, "", rows[0].GpuName)
	require.Equal(t, "", rows[0].KernelVersion)
	require.Equal(t, "", rows[0].Virtualization)
}
