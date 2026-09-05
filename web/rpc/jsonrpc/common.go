package jsonrpc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/pkg/selfupdate"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"

	cache "github.com/patrickmn/go-cache"
)

// pingstats:<uuid>
var pingStatsCache = cache.New(1*time.Minute, 2*time.Minute)

type pingStat struct {
	Name   string  `json:"name"`
	Latest int     `json:"latest"`
	Avg    int     `json:"avg"`
	Tail   float64 `json:"tail"` // (P99-P50)/P50
	Loss   float64 `json:"loss"` // 丢包率 %
	Min    int     `json:"min"`
	Max    int     `json:"max"`
}

// getPingStatsForNode 计算并缓存节点最近 1 小时 ping 统计
func getPingStatsForNode(uuid string, pingTasks []models.PingTask) map[string]pingStat {
	if uuid == "" {
		return map[string]pingStat{}
	}
	// 筛选属于该节点的任务
	assigned := make([]models.PingTask, 0, 4)
	for _, t := range pingTasks {
		if t.AppliesToClient(uuid) {
			assigned = append(assigned, t)
		}
	}
	assignmentIDs := make([]string, 0, len(assigned))
	for _, task := range assigned {
		assignmentIDs = append(assignmentIDs, fmt.Sprint(task.Id))
	}
	key := fmt.Sprintf("pingstats:%s:%s", uuid, strings.Join(assignmentIDs, ","))
	if v, ok := pingStatsCache.Get(key); ok {
		if m, ok2 := v.(map[string]pingStat); ok2 {
			return m
		}
	}
	if len(assigned) == 0 {
		empty := map[string]pingStat{}
		pingStatsCache.Set(key, empty, cache.DefaultExpiration)
		return empty
	}
	end := time.Now().UTC()
	start := end.Add(-1 * time.Hour)
	recs, err := tasks.GetPingRecords(uuid, -1, start, end)
	if err != nil || len(recs) == 0 {
		empty := map[string]pingStat{}
		pingStatsCache.Set(key, empty, cache.DefaultExpiration)
		return empty
	}
	grouped := make(map[uint][]models.PingRecord)
	for _, r := range recs {
		for _, t := range assigned {
			if r.TaskId == t.Id {
				grouped[r.TaskId] = append(grouped[r.TaskId], r)
				break
			}
		}
	}
	result := make(map[string]pingStat, len(grouped))
	for _, t := range assigned {
		records := grouped[t.Id]
		if len(records) == 0 {
			continue
		}
		latest := -1
		var latestTs time.Time
		values := make([]int, 0, len(records))
		sum := 0
		valid := 0
		total := 0
		lossCount := 0
		minLat := 0
		maxLat := 0
		for _, r := range records {
			total++
			if r.Value < 0 { // 丢包
				lossCount++
				continue
			}
			values = append(values, r.Value)
			sum += r.Value
			valid++
			if minLat == 0 || r.Value < minLat {
				minLat = r.Value
			}
			if r.Value > maxLat {
				maxLat = r.Value
			}
			ts := r.Time
			if latestTs.IsZero() || ts.After(latestTs) {
				latestTs = ts
				latest = r.Value
			}
		}
		avg := 0
		if valid > 0 {
			avg = sum / valid
		}
		p50, p99 := 0, 0
		if len(values) > 0 {
			sort.Ints(values)
			percentile := func(vals []int, pct float64) int {
				if len(vals) == 0 {
					return 0
				}
				if pct <= 0 {
					return vals[0]
				}
				if pct >= 1 {
					return vals[len(vals)-1]
				}
				pos := (float64(len(vals) - 1)) * pct
				lo := int(math.Floor(pos))
				hi := int(math.Ceil(pos))
				if lo == hi {
					return vals[lo]
				}
				frac := pos - float64(lo)
				v := float64(vals[lo]) + (float64(vals[hi])-float64(vals[lo]))*frac
				return int(math.Round(v))
			}
			p50 = percentile(values, 0.50)
			p99 = percentile(values, 0.99)
		}
		tail := 0.0
		if p50 > 0 && p99 >= p50 {
			tail = float64(p99-p50) / float64(p50)
		}
		lossRate := 0.0
		if total > 0 {
			lossRate = float64(lossCount) / float64(total) * 100
		}
		result[fmt.Sprintf("%d", t.Id)] = pingStat{
			Name:   t.Name,
			Latest: latest,
			Avg:    avg,
			Tail:   tail,
			Loss:   lossRate,
			Min:    minLat,
			Max:    maxLat,
		}
	}
	pingStatsCache.Set(key, result, cache.DefaultExpiration)
	return result
}

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
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get client info", err.Error())
	}
	meta := rpc.MetaFromContext(ctx)
	isAdmin := meta != nil && meta.Principal != nil && meta.Principal.HasRole(rpc.RoleAdmin)
	sendIPAddrToGuest, _ := config.GetAs[bool](config.SendIpAddrToGuestKey)
	nodes := presentThemeNodes(cinfo, isAdmin, sendIPAddrToGuest)
	if params.UUID != "" {
		node, ok := themeNodeByUUID(nodes, params.UUID)
		if !ok {
			return nil, rpc.MakeError(rpc.InvalidParams, "Node not found", params.UUID)
		}
		return node, nil
	}
	return themeNodeMap(nodes), nil
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
		UUID    string   `json:"uuid"`
		UUIDs   []string `json:"uuids"`
		Compact bool     `json:"compact"`
	}
	req.BindParams(&params)

	meta := rpc.MetaFromContext(ctx)
	latest := agent_runtime.GetLatestReport() // map[string]*v2.Report (copy)
	// The compact admin view also shows billing-cycle usage. This helper is
	// cached for 15 seconds, so the 5-second status poll does not rescan SQLite.
	calibrated, _ := trafficledger.CurrentCalibratedCycleUsages(ctx, dbcore.GetDBInstance(), time.Now().UTC())
	onlineUUIDs := agent_runtime.GetAllOnlineUUIDs()
	onlineSet := make(map[string]bool, len(onlineUUIDs))
	for _, uuid := range onlineUUIDs {
		onlineSet[uuid] = true
	}

	// Hidden 过滤
	if meta.Principal == nil || !meta.Principal.HasRole(rpc.RoleAdmin) {
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
		Client         string              `json:"client"`
		Time           time.Time           `json:"time"`
		Cpu            float32             `json:"cpu"`
		Gpu            float32             `json:"gpu"`
		Ram            int64               `json:"ram"`
		RamTotal       int64               `json:"ram_total"`
		Swap           int64               `json:"swap"`
		SwapTotal      int64               `json:"swap_total"`
		Load           float32             `json:"load"`
		Load5          float32             `json:"load5"`
		Load15         float32             `json:"load15"`
		Temp           float32             `json:"temp"`
		Disk           int64               `json:"disk"`
		DiskTotal      int64               `json:"disk_total"`
		NetIn          int64               `json:"net_in"`
		NetOut         int64               `json:"net_out"`
		NetTotalUp     int64               `json:"net_total_up"`
		NetTotalDown   int64               `json:"net_total_down"`
		Process        int                 `json:"process"`
		Connections    int                 `json:"connections"`
		ConnectionsUdp int                 `json:"connections_udp"`
		Online         bool                `json:"online"`
		Uptime         int64               `json:"uptime"`
		Ping           map[string]pingStat `json:"ping"`
	}

	respMap := make(map[string]recordLike, len(latest))

	// 精简模式跳过 Ping 汇总；本周期流量使用已有的 15 秒校准缓存。
	var pingTasks []models.PingTask
	if !params.Compact {
		pingTasks, _ = tasks.GetAllPingTasks()
	}

	appendOne := func(uuid string, rep *v2.Report) {
		if rep == nil {
			return
		}
		var stats map[string]pingStat
		if !params.Compact {
			stats = getPingStatsForNode(uuid, pingTasks)
		}
		totalUp, totalDown := rep.Network.TotalUp, rep.Network.TotalDown
		if usage, ok := calibrated[uuid]; ok {
			totalUp, totalDown = usage.Up, usage.Down
		}
		rl := recordLike{
			Client:         uuid,
			Time:           rep.UpdatedAt,
			Cpu:            float32(rep.CPU.Usage),
			Gpu:            0,
			Ram:            rep.Ram.Used,
			RamTotal:       rep.Ram.Total,
			Swap:           rep.Swap.Used,
			SwapTotal:      rep.Swap.Total,
			Load:           float32(rep.Load.Load1),
			Load5:          float32(rep.Load.Load5),
			Load15:         float32(rep.Load.Load15),
			Temp:           0,
			Disk:           rep.Disk.Used,
			DiskTotal:      rep.Disk.Total,
			NetIn:          rep.Network.Down,
			NetOut:         rep.Network.Up,
			NetTotalUp:     totalUp,
			NetTotalDown:   totalDown,
			Process:        rep.Process,
			Connections:    rep.Connections.TCP + rep.Connections.UDP,
			ConnectionsUdp: rep.Connections.UDP,
			Online:         onlineSet[uuid],
			Uptime:         rep.Uptime,
			Ping:           stats,
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
		Language     string `json:"language"`
		Color        string `json:"color"`
	}

	meta := rpc.MetaFromContext(ctx)

	switch meta.Principal.Type {
	case rpc.PrincipalUser, rpc.PrincipalAPIKey:
		if meta.User == nil {
			resp.LoggedIn = true
			resp.Username = "api_key"
			return resp, nil
		}
		resp.TwoFAEnabled = meta.User.TwoFactor != ""
		resp.LoggedIn = true
		resp.SSOId = meta.User.SSOID
		resp.SSOType = meta.User.SSOType
		resp.Username = meta.User.Username
		resp.UUID = meta.User.UUID
		resp.Language = meta.User.Language
		resp.Color = meta.User.Color
		return resp, nil
	case rpc.PrincipalAnonymous:
		resp.LoggedIn = false
		return resp, nil
	case rpc.PrincipalAgent:
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
		resp.LoggedIn = false
		return resp, nil
	}
}

func getVersion(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return struct {
		Version    string `json:"version"`
		Hash       string `json:"hash"`
		Deployment string `json:"deployment"`
	}{
		Version:    utils.CurrentVersion,
		Hash:       utils.VersionHash,
		Deployment: selfupdate.DeploymentType(),
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
	if meta.Principal != nil && meta.Principal.HasRole(rpc.RoleAdmin) {
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

	reports := agent_runtime.GetRecentReports(params.UUID)
	if usage, ok := calibratedUsageForClient(ctx, params.UUID); ok && len(reports) > 0 {
		newestUp := reports[len(reports)-1].Network.TotalUp
		newestDown := reports[len(reports)-1].Network.TotalDown
		for index := range reports {
			reports[index].Network.TotalUp = trafficledger.ShiftCumulativeCounter(reports[index].Network.TotalUp, newestUp, usage.Up)
			reports[index].Network.TotalDown = trafficledger.ShiftCumulativeCounter(reports[index].Network.TotalDown, newestDown, usage.Down)
		}
	}

	// 扁平化为 { count, records: [] }
	type flatRecord struct {
		Client         string    `json:"client"`
		Time           time.Time `json:"time"`
		Cpu            float32   `json:"cpu"`
		Gpu            float32   `json:"gpu"`
		Ram            int64     `json:"ram"`
		RamTotal       int64     `json:"ram_total"`
		Swap           int64     `json:"swap"`
		SwapTotal      int64     `json:"swap_total"`
		Load           float32   `json:"load"`
		Temp           float32   `json:"temp"`
		Disk           int64     `json:"disk"`
		DiskTotal      int64     `json:"disk_total"`
		NetIn          int64     `json:"net_in"`
		NetOut         int64     `json:"net_out"`
		NetTotalUp     int64     `json:"net_total_up"`
		NetTotalDown   int64     `json:"net_total_down"`
		Process        int       `json:"process"`
		Connections    int       `json:"connections"`
		ConnectionsUdp int       `json:"connections_udp"`
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
			Time:           r.UpdatedAt,
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

func calibratedUsageForClient(ctx context.Context, uuid string) (trafficledger.Usage, bool) {
	values, err := trafficledger.CurrentCalibratedCycleUsages(ctx, dbcore.GetDBInstance(), time.Now().UTC())
	if err != nil {
		return trafficledger.Usage{}, false
	}
	usage, ok := values[uuid]
	return usage, ok
}
