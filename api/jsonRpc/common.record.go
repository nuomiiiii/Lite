package jsonRpc

import (
	"context"
	"sort"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	recordsdb "github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils/rpc"
)

func init() {
	Register("getRecords", getRecords)
}

func getRecords(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	meta := rpc.MetaFromContext(ctx)
	var params struct {
		Type     string `json:"type"`      // "load" | "ping"; default "load"
		UUID     string `json:"uuid"`      // client uuid; empty = all clients
		Hours    int    `json:"hours"`     // time window in hours; default 1 if start/end not provided
		Start    string `json:"start"`     // RFC3339 start time (optional)
		End      string `json:"end"`       // RFC3339 end time (optional)
		LoadType string `json:"load_type"` // for type=load: cpu|gpu|ram|swap|load|temp|disk|network|process|connections|all
		TaskID   int    `json:"task_id"`   // for type=ping: optional task id; -1 or omitted means all
	}
	req.BindParams(&params)

	// defaults
	if params.Type == "" {
		params.Type = "load"
	}
	// parse time window
	var startTime, endTime time.Time
	if params.Start != "" || params.End != "" {
		// allow partial: missing end means now
		var err error
		if params.End == "" {
			endTime = time.Now()
		} else {
			endTime, err = time.Parse(time.RFC3339, params.End)
			if err != nil {
				return nil, rpc.MakeError(rpc.InvalidParams, "Invalid end time", params.End)
			}
		}
		if params.Start == "" {
			// default to 1 hour before end
			startTime = endTime.Add(-1 * time.Hour)
		} else {
			start, err := time.Parse(time.RFC3339, params.Start)
			if err != nil {
				return nil, rpc.MakeError(rpc.InvalidParams, "Invalid start time", params.Start)
			}
			startTime = start
		}
	} else {
		hours := params.Hours
		if hours <= 0 {
			hours = 1 // default 1 hour
		}
		endTime = time.Now()
		startTime = endTime.Add(-time.Duration(hours) * time.Hour)
	}

	// Hidden filtering for non-admin
	isAdmin := meta.Permission == "admin"
	hidden := map[string]bool{}
	if !isAdmin {
		cinfo, err := clients.GetAllClientBasicInfo()
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to get client info", err.Error())
		}
		for _, c := range cinfo {
			if c.Hidden {
				hidden[c.UUID] = true
			}
		}
		if params.UUID != "" && hidden[params.UUID] {
			return nil, rpc.MakeError(rpc.InvalidParams, "UUID not found", params.UUID)
		}
	}

	switch params.Type {
	case "load":
		// fetch load records
		recs, err := getLoadRecordsCombined(params.UUID, startTime, endTime)
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch records", err.Error())
		}
		// hidden filter on non-admin
		if !isAdmin {
			filtered := recs[:0]
			for _, r := range recs {
				if hidden[r.Client] {
					continue
				}
				filtered = append(filtered, r)
			}
			recs = filtered
		}

		// optional load_type filtering
		if params.LoadType != "" && params.LoadType != "all" {
			items := filterRecordsByLoadType(recs, params.LoadType)
			// stable sort by time
			sort.Slice(items, func(i, j int) bool { return items[i].Time.ToTime().Before(items[j].Time.ToTime()) })
			return struct {
				Count    int              `json:"count"`
				Records  []flatRecord     `json:"records"`
				LoadType string           `json:"load_type"`
				From     models.LocalTime `json:"from"`
				To       models.LocalTime `json:"to"`
			}{Count: len(items), Records: items, LoadType: params.LoadType, From: models.FromTime(startTime), To: models.FromTime(endTime)}, nil
		}
		// default: return full records
		sort.Slice(recs, func(i, j int) bool { return recs[i].Time.ToTime().Before(recs[j].Time.ToTime()) })
		return struct {
			Count   int              `json:"count"`
			Records []models.Record  `json:"records"`
			From    models.LocalTime `json:"from"`
			To      models.LocalTime `json:"to"`
		}{Count: len(recs), Records: recs, From: models.FromTime(startTime), To: models.FromTime(endTime)}, nil

	case "ping":
		taskId := params.TaskID
		if taskId == 0 {
			taskId = -1
		}
		recs, err := tasks.GetPingRecords(params.UUID, taskId, startTime, endTime)
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch ping records", err.Error())
		}
		// hidden filter
		if !isAdmin {
			filtered := recs[:0]
			for _, r := range recs {
				if r.Client != "" && hidden[r.Client] {
					continue
				}
				filtered = append(filtered, r)
			}
			recs = filtered
		}

		type RecordsResp struct {
			TaskId uint             `json:"task_id,omitempty"`
			Time   models.LocalTime `json:"time"`
			Value  int              `json:"value"`
			Client string           `json:"client,omitempty"`
		}
		type ClientBasicInfo struct {
			Client string  `json:"client"`
			Loss   float64 `json:"loss"`
			Min    int     `json:"min"`
			Max    int     `json:"max"`
		}
		type Resp struct {
			Count     int               `json:"count"`
			BasicInfo []ClientBasicInfo `json:"basic_info,omitempty"`
			Records   []RecordsResp     `json:"records"`
			Tasks     []map[string]any  `json:"tasks,omitempty"`
			From      models.LocalTime  `json:"from"`
			To        models.LocalTime  `json:"to"`
		}

		response := &Resp{Count: 0, Records: []RecordsResp{}, From: models.FromTime(startTime), To: models.FromTime(endTime)}

		// stats per client
		clientStats := make(map[string]struct {
			total int
			loss  int
			min   int
			max   int
		})

		for _, r := range recs {
			rr := RecordsResp{
				TaskId: r.TaskId,
				Time:   r.Time,
				Value:  r.Value,
				Client: r.Client,
			}
			st := clientStats[r.Client]
			st.total++
			if r.Value < 0 {
				st.loss++
			} else {
				if st.min == 0 || r.Value < st.min {
					st.min = r.Value
				}
				if r.Value > st.max {
					st.max = r.Value
				}
			}
			clientStats[r.Client] = st
			response.Records = append(response.Records, rr)
		}

		if len(clientStats) > 0 {
			response.BasicInfo = make([]ClientBasicInfo, 0, len(clientStats))
			for client, st := range clientStats {
				if client != "" && !isAdmin && hidden[client] {
					continue
				}
				loss := float64(0)
				if st.total > 0 {
					loss = float64(st.loss) / float64(st.total) * 100
				}
				response.BasicInfo = append(response.BasicInfo, ClientBasicInfo{
					Client: client,
					Loss:   loss,
					Min:    st.min,
					Max:    st.max,
				})
			}
		}

		// tasks summary
		if params.UUID != "" || taskId != -1 {
			pingTasks, err := tasks.GetAllPingTasks()
			if err != nil {
				return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch ping tasks", err.Error())
			}
			tlist := make([]map[string]any, 0, len(pingTasks))
			for _, t := range pingTasks {
				if taskId != -1 && t.Id != uint(taskId) {
					continue
				}
				if params.UUID != "" {
					// check assignment
					assigned := false
					for _, c := range t.Clients {
						if c == params.UUID {
							assigned = true
							break
						}
					}
					if !assigned {
						continue
					}
				}

				total := 0
				lossCount := 0
				minLat := 0
				maxLat := 0
				sum := 0
				valid := 0
				for _, r := range recs {
					if r.TaskId != t.Id {
						continue
					}
					if params.UUID != "" && r.Client != params.UUID {
						continue
					}
					total++
					if r.Value < 0 {
						lossCount++
					} else {
						valid++
						sum += r.Value
						if minLat == 0 || r.Value < minLat {
							minLat = r.Value
						}
						if r.Value > maxLat {
							maxLat = r.Value
						}
					}
				}
				avg := 0
				if valid > 0 {
					avg = sum / valid
				}
				info := map[string]any{
					"id":       t.Id,
					"name":     t.Name,
					"type":     t.Type,
					"interval": t.Interval,
					"loss": func() float64 {
						if total == 0 {
							return 0
						}
						return float64(lossCount) / float64(total) * 100
					}(),
					"min":   minLat,
					"max":   maxLat,
					"avg":   avg,
					"total": total,
				}
				if params.UUID == "" && taskId != -1 {
					info["clients"] = t.Clients
				}
				tlist = append(tlist, info)
			}
			response.Tasks = tlist
		}
		response.Count = len(response.Records)
		// sort by time asc
		sort.Slice(response.Records, func(i, j int) bool {
			return response.Records[i].Time.ToTime().Before(response.Records[j].Time.ToTime())
		})
		return response, nil
	default:
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid type, expected 'load' or 'ping'", params.Type)
	}
}

// ---------- helpers for load records ----------

// getLoadRecordsCombined fetches records for a client or all clients within a time range,
// combining recent short-term table and long-term table with 15-min grouping for recent part.
func getLoadRecordsCombined(uuid string, start, end time.Time) ([]models.Record, error) {
	// prefer the existing function when uuid provided
	if uuid != "" {
		return recordsdb.GetRecordsByClientAndTime(uuid, start, end)
	}
	db := dbcore.GetDBInstance()
	fourHoursAgo := time.Now().Add(-4*time.Hour - time.Minute)

	var recent []models.Record
	recentStart := start
	if end.After(fourHoursAgo) {
		if recentStart.Before(fourHoursAgo) {
			recentStart = fourHoursAgo
		}
		_ = db.Table("records").Where("time >= ? AND time <= ?", recentStart, end).Order("time ASC").Find(&recent).Error
	}

	var longTerm []models.Record
	_ = db.Table("records_long_term").Where("time >= ? AND time <= ?", start, end).Order("time ASC").Find(&longTerm).Error

	// if no long term, return all recent
	if len(longTerm) == 0 {
		return recent, nil
	}

	// group recent by client+15min, keep latest in bucket
	type key struct {
		c    string
		slot string
	}
	grouped := make(map[key]models.Record)
	for _, rec := range recent {
		k := key{c: rec.Client, slot: rec.Time.ToTime().Truncate(15 * time.Minute).Format(time.RFC3339)}
		if old, ok := grouped[k]; !ok || rec.Time.ToTime().After(old.Time.ToTime()) {
			grouped[k] = rec
		}
	}
	flat := make([]models.Record, 0, len(grouped))
	for _, rec := range grouped {
		flat = append(flat, rec)
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i].Time.ToTime().Before(flat[j].Time.ToTime()) })
	flat = append(flat, longTerm...)
	return flat, nil
}

// flatRecord is a projection used when load_type is specified.
type flatRecord struct {
	Client         string           `json:"client"`
	Time           models.LocalTime `json:"time"`
	Cpu            *float32         `json:"cpu,omitempty"`
	Gpu            *float32         `json:"gpu,omitempty"`
	Ram            *int64           `json:"ram,omitempty"`
	RamTotal       *int64           `json:"ram_total,omitempty"`
	Swap           *int64           `json:"swap,omitempty"`
	SwapTotal      *int64           `json:"swap_total,omitempty"`
	Load           *float32         `json:"load,omitempty"`
	Temp           *float32         `json:"temp,omitempty"`
	Disk           *int64           `json:"disk,omitempty"`
	DiskTotal      *int64           `json:"disk_total,omitempty"`
	NetIn          *int64           `json:"net_in,omitempty"`
	NetOut         *int64           `json:"net_out,omitempty"`
	NetTotalUp     *int64           `json:"net_total_up,omitempty"`
	NetTotalDown   *int64           `json:"net_total_down,omitempty"`
	Process        *int             `json:"process,omitempty"`
	Connections    *int             `json:"connections,omitempty"`
	ConnectionsUdp *int             `json:"connections_udp,omitempty"`
}

func filterRecordsByLoadType(recs []models.Record, loadType string) []flatRecord {
	out := make([]flatRecord, 0, len(recs))
	for _, r := range recs {
		fr := flatRecord{Client: r.Client, Time: r.Time}
		switch loadType {
		case "cpu":
			v := r.Cpu
			fr.Cpu = &v
		case "gpu":
			v := r.Gpu
			fr.Gpu = &v
		case "ram":
			v := r.Ram
			fr.Ram = &v
			vt := r.RamTotal
			fr.RamTotal = &vt
		case "swap":
			v := r.Swap
			fr.Swap = &v
			vt := r.SwapTotal
			fr.SwapTotal = &vt
		case "load":
			v := r.Load
			fr.Load = &v
		case "temp":
			v := r.Temp
			fr.Temp = &v
		case "disk":
			v := r.Disk
			fr.Disk = &v
			vt := r.DiskTotal
			fr.DiskTotal = &vt
		case "network":
			vi := r.NetIn
			vo := r.NetOut
			vtu := r.NetTotalUp
			vtd := r.NetTotalDown
			fr.NetIn = &vi
			fr.NetOut = &vo
			fr.NetTotalUp = &vtu
			fr.NetTotalDown = &vtd
		case "process":
			v := r.Process
			fr.Process = &v
		case "connections":
			v := r.Connections
			fr.Connections = &v
			vu := r.ConnectionsUdp
			fr.ConnectionsUdp = &vu
		default:
			// unknown type: fallback to all fields as a full record would be returned elsewhere
			v := r.Cpu
			fr.Cpu = &v
		}
		out = append(out, fr)
	}
	return out
}
