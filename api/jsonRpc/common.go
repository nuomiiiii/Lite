package jsonRpc

import (
	"context"

	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/utils/rpc"
	"github.com/komari-monitor/komari/ws"
)

func init() {
	RegisterWithGroupAndMeta("getNodes", "common",
		func(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
			return getNodes(ctx, req)
		},
		&rpc.MethodMeta{
			Name:    "getNodes",
			Summary: "Get all nodes",
			Params: []rpc.ParamMeta{
				{
					Name:        "uuid",
					Description: "Specify the UUID of the node",
					Required:    false,
					Type:        "string",
				},
			},
			Returns: "Client | { [uuid]: Client }",
		},
	)
	RegisterWithGroupAndMeta("getNodesLatestStatus", "common",
		func(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
			return getNodesLatestStatus(ctx, req)
		},
		&rpc.MethodMeta{
			Name:    "getNodesLatestStatus",
			Summary: "Get latest status reports (single node or map).",
			Params: []rpc.ParamMeta{
				{
					Name:        "uuid",
					Description: "Specify the UUID of the node (optional)",
					Required:    false,
					Type:        "string",
				},
				{
					Name:        "uuids",
					Description: "Specify multiple UUIDs (array) to get subset (ignored if uuid provided)",
					Required:    false,
					Type:        "string[]",
				},
			},
			Returns: "Record | { [uuid]: Record }",
		},
	)
	Register("getMe", func(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
		return getMe(ctx, req)
	})
	Register("getPublicInfo", getPublicInfo)
	Register("getVersion", getVersion)
	Register("getNodeRecentStatus", getNodeRecentStatus)
}

func getNodes(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID string `json:"uuid"`
	}
	req.BindParams(&params)
	cinfo, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get client info", cinfo)
	}
	meta := rpc.MetaFromContext(ctx)

	if meta.Permission != "admin" {
		// 过滤 Hidden 节点并隐藏敏感字段
		filtered := make([]models.Client, 0, len(cinfo))
		for _, node := range cinfo {
			if node.Hidden { // 非 admin 不显示隐藏节点
				continue
			}
			node.IPv4 = ""
			node.IPv6 = ""
			node.Remark = ""
			node.Version = ""
			node.Token = ""
			filtered = append(filtered, node)
		}
		cinfo = filtered
	}
	if params.UUID != "" {
		for _, node := range cinfo {
			if node.UUID == params.UUID {
				return node, nil
			}
		}
		return nil, rpc.MakeError(rpc.InvalidParams, "Node not found", params.UUID)
	}

	nodesMap := make(map[string]models.Client, len(cinfo))
	for _, node := range cinfo {
		nodesMap[node.UUID] = node
	}
	return nodesMap, nil
}

func getPublicInfo(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	info, err := database.GetPublicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get public info", err.Error())
	}
	return info, nil
}

func getNodesLatestStatus(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID  string   `json:"uuid"`
		UUIDs []string `json:"uuids"`
	}
	req.BindParams(&params)

	meta := rpc.MetaFromContext(ctx)
	latest := ws.GetLatestReport() // map[string]*common.Report (copy)
	connected := ws.GetConnectedClients()
	onlineSet := make(map[string]bool, len(connected))
	for uuid := range connected {
		onlineSet[uuid] = true
	}

	// Hidden 过滤
	if meta.Permission != "admin" {
		cinfo, err := clients.GetAllClientBasicInfo()
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to get client info", err.Error())
		}
		hidden := make(map[string]bool, len(cinfo))
		for _, c := range cinfo {
			if c.Hidden {
				hidden[c.UUID] = true
			}
		}
		for uuid := range latest {
			if hidden[uuid] {
				delete(latest, uuid)
			}
		}
	}

	// 如果指定 uuid 但找不到，直接返回 not found
	if params.UUID != "" {
		if _, ok := latest[params.UUID]; !ok {
			return nil, rpc.MakeError(rpc.InvalidParams, "Node not found", params.UUID)
		}
	}

	type recordLike struct {
		Client         string           `json:"client"`
		Time           models.LocalTime `json:"time"`
		Cpu            float32          `json:"cpu"`
		Gpu            float32          `json:"gpu"`
		Ram            int64            `json:"ram"`
		RamTotal       int64            `json:"ram_total"`
		Swap           int64            `json:"swap"`
		SwapTotal      int64            `json:"swap_total"`
		Load           float32          `json:"load"`
		Load5          float32          `json:"load5"`
		Load15         float32          `json:"load15"`
		Temp           float32          `json:"temp"`
		Disk           int64            `json:"disk"`
		DiskTotal      int64            `json:"disk_total"`
		NetIn          int64            `json:"net_in"`
		NetOut         int64            `json:"net_out"`
		NetTotalUp     int64            `json:"net_total_up"`
		NetTotalDown   int64            `json:"net_total_down"`
		Process        int              `json:"process"`
		Connections    int              `json:"connections"`
		ConnectionsUdp int              `json:"connections_udp"`
		Online         bool             `json:"online"`
		Uptime         int64            `json:"uptime"` // seconds
	}

	respMap := make(map[string]recordLike, len(latest))
	appendOne := func(uuid string, rep *common.Report) {
		if rep == nil {
			return
		}
		// time 使用 UpdatedAt
		rl := recordLike{
			Client:         uuid,
			Time:           models.FromTime(rep.UpdatedAt),
			Cpu:            float32(rep.CPU.Usage),
			Gpu:            0, // 暂无实时 GPU 数据
			Ram:            rep.Ram.Used,
			RamTotal:       rep.Ram.Total,
			Swap:           rep.Swap.Used,
			SwapTotal:      rep.Swap.Total,
			Load:           float32(rep.Load.Load1),
			Load5:          float32(rep.Load.Load5),
			Load15:         float32(rep.Load.Load15),
			Temp:           0, // 没有温度字段
			Disk:           rep.Disk.Used,
			DiskTotal:      rep.Disk.Total,
			NetIn:          rep.Network.Down,
			NetOut:         rep.Network.Up,
			NetTotalUp:     rep.Network.TotalUp,
			NetTotalDown:   rep.Network.TotalDown,
			Process:        rep.Process,
			Connections:    rep.Connections.TCP + rep.Connections.UDP,
			ConnectionsUdp: rep.Connections.UDP,
			Online:         onlineSet[uuid],
			Uptime:         rep.Uptime,
		}
		respMap[uuid] = rl
	}

	// 选择逻辑
	if params.UUID != "" { // 单个
		appendOne(params.UUID, latest[params.UUID])
		return respMap[params.UUID], nil
	}
	selected := map[string]bool{}
	if len(params.UUIDs) > 0 {
		for _, id := range params.UUIDs {
			selected[id] = true
		}
		for uuid, rep := range latest {
			if selected[uuid] {
				appendOne(uuid, rep)
			}
		}
		return respMap, nil
	}
	for uuid, rep := range latest {
		appendOne(uuid, rep)
	}
	return respMap, nil
}

func getMe(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var resp struct {
		TwoFAEnabled bool   `json:"2fa_enabled"`
		LoggedIn     bool   `json:"logged_in"`
		SSOId        string `json:"sso_id"`
		SSOType      string `json:"sso_type"`
		Username     string `json:"username"`
		UUID         string `json:"uuid"`
	}

	meta := rpc.MetaFromContext(ctx)

	switch meta.Permission {
	case "admin":
		resp.TwoFAEnabled = meta.User.TwoFactor != ""
		resp.LoggedIn = true
		resp.SSOId = meta.User.SSOID
		resp.SSOType = meta.User.SSOType
		resp.Username = meta.User.Username
		resp.UUID = meta.User.UUID
		return resp, nil
	case "guest":
		resp.LoggedIn = false
		return resp, nil
	case "client":
		resp.LoggedIn = true
		resp.SSOId = "client"
		resp.SSOType = "client"
		resp.Username = "client"
		resp.UUID = meta.ClientToken
		client, err := clients.GetClientUUIDByToken(meta.ClientToken)
		if err != nil {
			resp.UUID = client
		}
		return resp, nil
	default:
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid user role", meta.Permission)
	}
}

func getVersion(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return struct {
		Version string `json:"version"`
		Hash    string `json:"hash"`
	}{
		Version: utils.CurrentVersion,
		Hash:    utils.VersionHash,
	}, nil
}

func getNodeRecentStatus(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID string `json:"uuid"`
	}
	req.BindParams(&params)
	if params.UUID == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "UUID is required", params)
	}
	meta := rpc.MetaFromContext(ctx)
	// 登录状态检查
	isLogin := false
	if meta.Permission == "admin" {
		isLogin = true
	}

	// 仅在未登录时需要 Hidden 信息做过滤
	hiddenMap := map[string]bool{}
	if !isLogin {
		var hiddenClients []models.Client
		db := dbcore.GetDBInstance()
		_ = db.Select("uuid").Where("hidden = ?", true).Find(&hiddenClients).Error
		for _, cli := range hiddenClients {
			hiddenMap[cli.UUID] = true
		}

		if hiddenMap[params.UUID] {
			return nil, rpc.MakeError(rpc.InvalidParams, "UUID is required", params) //防止未登录用户获取隐藏客户端数据
		}
	}

	raw, _ := api.Records.Get(params.UUID)
	reports, _ := raw.([]common.Report)

	// 扁平化为 { count, records: [] }
	type flatRecord struct {
		Client         string           `json:"client"`
		Time           models.LocalTime `json:"time"`
		Cpu            float32          `json:"cpu"`
		Gpu            float32          `json:"gpu"`
		Ram            int64            `json:"ram"`
		RamTotal       int64            `json:"ram_total"`
		Swap           int64            `json:"swap"`
		SwapTotal      int64            `json:"swap_total"`
		Load           float32          `json:"load"`
		Temp           float32          `json:"temp"`
		Disk           int64            `json:"disk"`
		DiskTotal      int64            `json:"disk_total"`
		NetIn          int64            `json:"net_in"`
		NetOut         int64            `json:"net_out"`
		NetTotalUp     int64            `json:"net_total_up"`
		NetTotalDown   int64            `json:"net_total_down"`
		Process        int              `json:"process"`
		Connections    int              `json:"connections"`
		ConnectionsUdp int              `json:"connections_udp"`
	}

	resp := struct {
		Count   int          `json:"count"`
		Records []flatRecord `json:"records"`
	}{
		Count:   0,
		Records: []flatRecord{},
	}

	if len(reports) == 0 {
		return resp, nil
	}

	resp.Records = make([]flatRecord, 0, len(reports))
	for _, r := range reports {
		fr := flatRecord{
			Client:         params.UUID,
			Time:           models.FromTime(r.UpdatedAt),
			Cpu:            float32(r.CPU.Usage),
			Gpu:            0,
			Ram:            r.Ram.Used,
			RamTotal:       r.Ram.Total,
			Swap:           r.Swap.Used,
			SwapTotal:      r.Swap.Total,
			Load:           float32(r.Load.Load1),
			Temp:           0,
			Disk:           r.Disk.Used,
			DiskTotal:      r.Disk.Total,
			NetIn:          r.Network.Down,
			NetOut:         r.Network.Up,
			NetTotalUp:     r.Network.TotalUp,
			NetTotalDown:   r.Network.TotalDown,
			Process:        r.Process,
			Connections:    r.Connections.TCP + r.Connections.UDP,
			ConnectionsUdp: r.Connections.UDP,
		}
		resp.Records = append(resp.Records, fr)
	}
	resp.Count = len(resp.Records)
	return resp, nil
}
