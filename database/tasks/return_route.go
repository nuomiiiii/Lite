package tasks

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/utils/messageSender"
	"gorm.io/gorm"
)

const returnRouteEventRetention = 90 * 24 * time.Hour

type ReturnRouteOverview struct {
	Tasks    []models.ReturnRouteTask   `json:"tasks"`
	Statuses []models.ReturnRouteStatus `json:"statuses"`
	Events   []models.ReturnRouteEvent  `json:"events"`
}

type ReturnRouteTaskQuery struct {
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Keyword  string `json:"keyword"`
	Carrier  string `json:"carrier"`
	State    string `json:"state"`
}

type ReturnRouteTaskPage struct {
	Tasks    []models.ReturnRouteTask   `json:"tasks"`
	Statuses []models.ReturnRouteStatus `json:"statuses"`
	Total    int64                      `json:"total"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"page_size"`
}

type ReturnRouteEventQuery struct {
	Page         int        `json:"page"`
	PageSize     int        `json:"page_size"`
	Keyword      string     `json:"keyword"`
	Kind         string     `json:"kind"`
	Carrier      string     `json:"carrier"`
	Region       string     `json:"region"`
	ExpectedLine string     `json:"expected_line"`
	ActualLine   string     `json:"actual_line"`
	Start        *time.Time `json:"start"`
	End          *time.Time `json:"end"`
}

type ReturnRouteEventItem struct {
	models.ReturnRouteEvent
	NodeName string `json:"node_name"`
}

type ReturnRouteEventPage struct {
	Events   []ReturnRouteEventItem `json:"events"`
	Total    int64                  `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"page_size"`
}

func normalizeReturnRouteTask(task *models.ReturnRouteTask) error {
	task.Name = strings.TrimSpace(task.Name)
	task.Client = strings.TrimSpace(task.Client)
	task.Carrier = strings.ToLower(strings.TrimSpace(task.Carrier))
	task.Region = strings.TrimSpace(task.Region)
	task.Target = strings.TrimSpace(task.Target)
	task.ExpectedLine = strings.ToUpper(strings.TrimSpace(task.ExpectedLine))
	task.Protocol = strings.ToLower(strings.TrimSpace(task.Protocol))
	if task.Name == "" || task.Client == "" || task.Target == "" || task.ExpectedLine == "" {
		return fmt.Errorf("name, client, target and expected_line are required")
	}
	if task.Carrier != "mobile" && task.Carrier != "telecom" && task.Carrier != "unicom" {
		return fmt.Errorf("unsupported carrier %q", task.Carrier)
	}
	if task.IPVersion != 4 && task.IPVersion != 6 {
		return fmt.Errorf("ip_version must be 4 or 6")
	}
	if task.Protocol == "" {
		task.Protocol = "icmp"
	}
	if task.Protocol != "icmp" {
		return fmt.Errorf("snapshot currently supports the built-in ICMP route probe")
	}
	validLine := false
	for _, line := range returnRouteLines() {
		if task.ExpectedLine == line {
			validLine = true
			break
		}
	}
	if !validLine {
		return fmt.Errorf("unsupported expected_line %q", task.ExpectedLine)
	}
	if task.Interval < 60 || task.Interval > 86400 {
		return fmt.Errorf("interval must be between 60 and 86400 seconds")
	}
	if task.SwitchConfirm < 1 || task.SwitchConfirm > 20 || task.RecoveryConfirm < 1 || task.RecoveryConfirm > 20 {
		return fmt.Errorf("confirmation counts must be between 1 and 20")
	}
	if task.Cooldown < 0 || task.Cooldown > 604800 {
		return fmt.Errorf("cooldown must be between 0 and 604800 seconds")
	}
	var count int64
	if err := dbcore.GetDBInstance().Model(&models.Client{}).Where("uuid = ?", task.Client).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("client not found")
	}
	return nil
}

func returnRouteLines() []string {
	return []string{"CMIN2", "CMI", "CMNET", "CN2 GIA", "CN2 GT", "163", "9929", "4837"}
}

func AddReturnRouteTask(task *models.ReturnRouteTask) (uint, bool, error) {
	if err := normalizeReturnRouteTask(task); err != nil {
		return 0, false, err
	}
	if err := dbcore.GetDBInstance().Create(task).Error; err != nil {
		return 0, false, err
	}
	if err := ReloadReturnRouteSchedule(); err != nil {
		return task.Id, false, err
	}
	dispatched := task.Enabled && utils.DispatchReturnRouteTask(*task)
	return task.Id, dispatched, nil
}

func EditReturnRouteTask(task *models.ReturnRouteTask) error {
	if task.Id == 0 {
		return fmt.Errorf("task id is required")
	}
	if err := normalizeReturnRouteTask(task); err != nil {
		return err
	}
	updates := map[string]any{
		"name": task.Name, "client": task.Client, "carrier": task.Carrier,
		"region": task.Region, "target": task.Target, "ip_version": task.IPVersion,
		"expected_line": task.ExpectedLine, "protocol": task.Protocol,
		"interval": task.Interval, "switch_confirm": task.SwitchConfirm,
		"recovery_confirm": task.RecoveryConfirm, "cooldown": task.Cooldown,
		"notify": task.Notify, "enabled": task.Enabled,
	}
	result := dbcore.GetDBInstance().Model(&models.ReturnRouteTask{}).Where("id = ?", task.Id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	_ = ReloadReturnRouteSchedule()
	return nil
}

func DeleteReturnRouteTasks(ids []uint) error {
	if len(ids) == 0 {
		return fmt.Errorf("task id is required")
	}
	err := dbcore.GetDBInstance().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id IN ?", ids).Delete(&models.ReturnRouteEvent{}).Error; err != nil {
			return err
		}
		if err := tx.Where("task_id IN ?", ids).Delete(&models.ReturnRouteStatus{}).Error; err != nil {
			return err
		}
		result := tx.Where("id IN ?", ids).Delete(&models.ReturnRouteTask{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err == nil {
		_ = ReloadReturnRouteSchedule()
	}
	return err
}

func GetReturnRouteOverview() (ReturnRouteOverview, error) {
	db := dbcore.GetDBInstance()
	result := ReturnRouteOverview{Tasks: []models.ReturnRouteTask{}, Statuses: []models.ReturnRouteStatus{}, Events: []models.ReturnRouteEvent{}}
	if err := db.Preload("ClientInfo").Order("id ASC").Find(&result.Tasks).Error; err != nil {
		return result, err
	}
	if err := db.Find(&result.Statuses).Error; err != nil {
		return result, err
	}
	if err := db.Order("occurred_at DESC").Limit(200).Find(&result.Events).Error; err != nil {
		return result, err
	}
	return result, nil
}

func QueryReturnRouteTasks(params ReturnRouteTaskQuery) (ReturnRouteTaskPage, error) {
	return queryReturnRouteTasks(dbcore.GetDBInstance(), params)
}

func queryReturnRouteTasks(db *gorm.DB, params ReturnRouteTaskQuery) (ReturnRouteTaskPage, error) {
	page, pageSize := normalizeReturnRoutePagination(params.Page, params.PageSize)
	query, err := filterReturnRouteTasks(db.Model(&models.ReturnRouteTask{}), params, db)
	result := ReturnRouteTaskPage{Tasks: []models.ReturnRouteTask{}, Statuses: []models.ReturnRouteStatus{}, Page: page, PageSize: pageSize}
	if err != nil {
		return result, err
	}
	if err := query.Count(&result.Total).Error; err != nil {
		return result, err
	}
	if err := query.Preload("ClientInfo").Order("id ASC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&result.Tasks).Error; err != nil {
		return result, err
	}
	ids := make([]uint, 0, len(result.Tasks))
	for _, task := range result.Tasks {
		ids = append(ids, task.Id)
	}
	if len(ids) > 0 {
		if err := db.Where("task_id IN ?", ids).Find(&result.Statuses).Error; err != nil {
			return result, err
		}
	}
	return result, nil
}

func filterReturnRouteTasks(query *gorm.DB, params ReturnRouteTaskQuery, db *gorm.DB) (*gorm.DB, error) {
	if keyword := strings.ToLower(strings.TrimSpace(params.Keyword)); keyword != "" {
		pattern := "%" + keyword + "%"
		clients := db.Model(&models.Client{}).Select("uuid").Where("LOWER(name) LIKE ?", pattern)
		query = query.Where("LOWER(name) LIKE ? OR LOWER(target) LIKE ? OR LOWER(region) LIKE ? OR LOWER(client) LIKE ? OR client IN (?)",
			pattern, pattern, pattern, pattern, clients)
	}
	if carrier := strings.ToLower(strings.TrimSpace(params.Carrier)); carrier != "" {
		if carrier != "mobile" && carrier != "telecom" && carrier != "unicom" {
			return query, fmt.Errorf("unsupported carrier %q", carrier)
		}
		query = query.Where("carrier = ?", carrier)
	}
	switch state := strings.ToLower(strings.TrimSpace(params.State)); state {
	case "":
	case "disabled":
		query = query.Where("enabled = ?", false)
	case "pending":
		allStatuses := db.Model(&models.ReturnRouteStatus{}).Select("task_id")
		pendingStatuses := db.Model(&models.ReturnRouteStatus{}).Select("task_id").Where("state = ?", "pending")
		query = query.Where("enabled = ?", true).Where("(id NOT IN (?) OR id IN (?))", allStatuses, pendingStatuses)
	case "observing", "healthy", "switched", "unknown":
		statuses := db.Model(&models.ReturnRouteStatus{}).Select("task_id").Where("state = ?", state)
		query = query.Where("id IN (?)", statuses)
	default:
		return query, fmt.Errorf("unsupported state %q", state)
	}
	return query, nil
}

func QueryReturnRouteEvents(params ReturnRouteEventQuery) (ReturnRouteEventPage, error) {
	return queryReturnRouteEvents(dbcore.GetDBInstance(), params)
}

func queryReturnRouteEvents(db *gorm.DB, params ReturnRouteEventQuery) (ReturnRouteEventPage, error) {
	page, pageSize := normalizeReturnRoutePagination(params.Page, params.PageSize)
	result := ReturnRouteEventPage{Events: []ReturnRouteEventItem{}, Page: page, PageSize: pageSize}
	query, err := filterReturnRouteEvents(db.Model(&models.ReturnRouteEvent{}), params, db)
	if err != nil {
		return result, err
	}
	if err := query.Count(&result.Total).Error; err != nil {
		return result, err
	}
	var events []models.ReturnRouteEvent
	if err := query.Order("occurred_at DESC, id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&events).Error; err != nil {
		return result, err
	}

	taskIDs := make([]uint, 0, len(events))
	seenTasks := map[uint]bool{}
	for _, event := range events {
		if !seenTasks[event.TaskId] {
			taskIDs = append(taskIDs, event.TaskId)
			seenTasks[event.TaskId] = true
		}
	}
	var tasks []models.ReturnRouteTask
	if len(taskIDs) > 0 {
		if err := db.Preload("ClientInfo").Where("id IN ?", taskIDs).Find(&tasks).Error; err != nil {
			return result, err
		}
	}
	taskByID := make(map[uint]models.ReturnRouteTask, len(tasks))
	for _, task := range tasks {
		taskByID[task.Id] = task
	}
	for _, event := range events {
		task := taskByID[event.TaskId]
		if event.TaskName == "" {
			event.TaskName = task.Name
		}
		if event.Carrier == "" {
			event.Carrier = task.Carrier
		}
		if event.Region == "" {
			event.Region = task.Region
		}
		if event.Target == "" {
			event.Target = task.Target
		}
		if event.IPVersion == 0 {
			event.IPVersion = task.IPVersion
		}
		if event.ExpectedLine == "" {
			event.ExpectedLine = task.ExpectedLine
		}
		result.Events = append(result.Events, ReturnRouteEventItem{ReturnRouteEvent: event, NodeName: task.ClientInfo.Name})
	}
	return result, nil
}

func filterReturnRouteEvents(query *gorm.DB, params ReturnRouteEventQuery, db *gorm.DB) (*gorm.DB, error) {
	if params.Start != nil {
		query = query.Where("occurred_at >= ?", params.Start.UTC())
	}
	if params.End != nil {
		query = query.Where("occurred_at < ?", params.End.UTC())
	}
	if params.Start != nil && params.End != nil && !params.End.After(*params.Start) {
		return query, fmt.Errorf("end must be after start")
	}
	if kind := strings.ToLower(strings.TrimSpace(params.Kind)); kind != "" {
		if kind != "switch" && kind != "recovery" {
			return query, fmt.Errorf("unsupported event kind %q", kind)
		}
		query = query.Where("kind = ?", kind)
	}
	if line := strings.ToUpper(strings.TrimSpace(params.ActualLine)); line != "" {
		if !isReturnRouteLine(line) {
			return query, fmt.Errorf("unsupported actual_line %q", line)
		}
		query = query.Where("to_line = ?", line)
	}

	if keyword := strings.ToLower(strings.TrimSpace(params.Keyword)); keyword != "" {
		pattern := "%" + keyword + "%"
		clients := db.Model(&models.Client{}).Select("uuid").Where("LOWER(name) LIKE ?", pattern)
		tasks := db.Model(&models.ReturnRouteTask{}).Select("id").Where(
			"LOWER(name) LIKE ? OR LOWER(target) LIKE ? OR LOWER(client) LIKE ? OR client IN (?)", pattern, pattern, pattern, clients,
		)
		query = query.Where("LOWER(task_name) LIKE ? OR LOWER(target) LIKE ? OR LOWER(asn_path) LIKE ? OR LOWER(route_path) LIKE ? OR task_id IN (?)",
			pattern, pattern, pattern, pattern, tasks)
	}
	if carrier := strings.ToLower(strings.TrimSpace(params.Carrier)); carrier != "" {
		if carrier != "mobile" && carrier != "telecom" && carrier != "unicom" {
			return query, fmt.Errorf("unsupported carrier %q", carrier)
		}
		tasks := db.Model(&models.ReturnRouteTask{}).Select("id").Where("carrier = ?", carrier)
		query = query.Where("carrier = ? OR (carrier = '' AND task_id IN (?))", carrier, tasks)
	}
	if region := strings.TrimSpace(params.Region); region != "" {
		tasks := db.Model(&models.ReturnRouteTask{}).Select("id").Where("region = ?", region)
		query = query.Where("region = ? OR (region = '' AND task_id IN (?))", region, tasks)
	}
	if line := strings.ToUpper(strings.TrimSpace(params.ExpectedLine)); line != "" {
		if !isReturnRouteLine(line) {
			return query, fmt.Errorf("unsupported expected_line %q", line)
		}
		tasks := db.Model(&models.ReturnRouteTask{}).Select("id").Where("expected_line = ?", line)
		query = query.Where("expected_line = ? OR (expected_line = '' AND task_id IN (?))", line, tasks)
	}
	return query, nil
}

func normalizeReturnRoutePagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func isReturnRouteLine(value string) bool {
	for _, line := range returnRouteLines() {
		if value == line {
			return true
		}
	}
	return false
}

func GetEnabledReturnRouteTasks() ([]models.ReturnRouteTask, error) {
	var list []models.ReturnRouteTask
	err := dbcore.GetDBInstance().Where("enabled = ?", true).Find(&list).Error
	return list, err
}

func SaveReturnRouteResult(client string, result v2.RouteResultParams) error {
	db := dbcore.GetDBInstance()
	var task models.ReturnRouteTask
	if err := db.Preload("ClientInfo").First(&task, result.TaskID).Error; err != nil {
		return err
	}
	if !task.Enabled || task.Client != client {
		return fmt.Errorf("return route task is not assigned to this client")
	}
	now := result.FinishedAt.UTC()
	if now.IsZero() || now.After(time.Now().UTC().Add(time.Minute)) {
		now = time.Now().UTC()
	}
	routePath := make(models.StringArray, 0, len(result.Hops))
	publicIPs := make([]string, 0, len(result.Hops))
	for _, hop := range result.Hops {
		if hop.Timeout || strings.TrimSpace(hop.IP) == "" {
			routePath = append(routePath, fmt.Sprintf("%d *", hop.TTL))
			continue
		}
		ip := strings.TrimSpace(hop.IP)
		routePath = append(routePath, fmt.Sprintf("%d %s %.1fms", hop.TTL, ip, hop.LatencyMS))
		publicIPs = append(publicIPs, ip)
	}
	asns := lookupASNs(publicIPs)
	asnPath := make(models.StringArray, 0, len(publicIPs))
	seen := map[int]bool{}
	for _, ip := range publicIPs {
		asn := asns[ip]
		if asn > 0 && !seen[asn] {
			asnPath = append(asnPath, fmt.Sprintf("AS%d", asn))
			seen[asn] = true
		}
	}
	line, confidence := classifyReturnRoute(asnPath)
	probeError := strings.TrimSpace(result.Error)
	if probeError == "" && len(publicIPs) == 0 {
		probeError = "no route hops were returned"
	}
	if probeError == "" && line == "UNKNOWN" {
		probeError = "route collected, but no carrier ASN was identified"
	}

	var event *models.ReturnRouteEvent
	err := db.Transaction(func(tx *gorm.DB) error {
		var status models.ReturnRouteStatus
		find := tx.First(&status, "task_id = ?", task.Id)
		if find.Error != nil && find.Error != gorm.ErrRecordNotFound {
			return find.Error
		}
		status.TaskId = task.Id
		status.LastCheckedAt = &now
		status.RoutePath = routePath
		status.ASNPath = asnPath
		status.Confidence = confidence
		status.LastError = probeError
		if probeError == "" && line != "UNKNOWN" {
			event = advanceReturnRouteState(&status, task, line, now)
		} else if status.CurrentLine == "" {
			status.State = "unknown"
		}
		if find.Error == gorm.ErrRecordNotFound {
			if err := tx.Create(&status).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&status).Error; err != nil {
			return err
		}
		if event != nil {
			if err := tx.Create(event).Error; err != nil {
				return err
			}
		}
		return tx.Where("occurred_at < ?", now.Add(-returnRouteEventRetention)).Delete(&models.ReturnRouteEvent{}).Error
	})
	if err != nil {
		return err
	}
	if event != nil && task.Notify {
		go sendReturnRouteNotification(task, *event)
	}
	return nil
}

func advanceReturnRouteState(status *models.ReturnRouteStatus, task models.ReturnRouteTask, line string, now time.Time) *models.ReturnRouteEvent {
	expected := strings.ToUpper(task.ExpectedLine)
	if status.CurrentLine == "" && line == expected {
		status.CurrentLine, status.State = line, "healthy"
		status.CandidateLine, status.CandidateCount = "", 0
		status.LastChangedAt = &now
		return nil
	}
	targetState := "switched"
	required := task.SwitchConfirm
	kind := "switch"
	from := status.CurrentLine
	if from == "" {
		from = expected
	}
	if line == expected {
		targetState = "healthy"
		required = task.RecoveryConfirm
		kind = "recovery"
		if status.State != "switched" {
			status.CurrentLine, status.State = line, "healthy"
			status.CandidateLine, status.CandidateCount = "", 0
			return nil
		}
	} else if status.State == "switched" && line == status.CurrentLine {
		status.CandidateLine, status.CandidateCount = "", 0
		return nil
	}
	if status.CandidateLine == line {
		status.CandidateCount++
	} else {
		status.CandidateLine, status.CandidateCount = line, 1
	}
	if status.CandidateCount < required {
		if status.CurrentLine == "" {
			status.State = "observing"
		}
		return nil
	}
	status.CurrentLine, status.State = line, targetState
	status.CandidateLine, status.CandidateCount = "", 0
	status.LastChangedAt = &now
	return &models.ReturnRouteEvent{
		TaskId: task.Id, Client: task.Client, TaskName: task.Name, Carrier: task.Carrier,
		Region: task.Region, Target: task.Target, IPVersion: task.IPVersion, ExpectedLine: expected,
		Kind: kind, FromLine: from, ToLine: line, Confidence: status.Confidence,
		ASNPath: append(models.StringArray{}, status.ASNPath...), RoutePath: append(models.StringArray{}, status.RoutePath...), OccurredAt: now,
	}
}

func sendReturnRouteNotification(task models.ReturnRouteTask, event models.ReturnRouteEvent) {
	db := dbcore.GetDBInstance()
	var status models.ReturnRouteStatus
	if err := db.First(&status, "task_id = ?", task.Id).Error; err != nil {
		return
	}
	if event.Kind == "switch" && status.LastNotifiedAt != nil && event.OccurredAt.Before(status.LastNotifiedAt.Add(time.Duration(task.Cooldown)*time.Second)) {
		return
	}
	title := "回程线路已切换"
	if event.Kind == "recovery" {
		title = "回程线路已恢复"
	}
	client := task.ClientInfo
	if client.UUID == "" {
		client.UUID = task.Client
	}
	message := formatReturnRouteNotification(task, event)
	if err := messageSender.SendEvent(models.EventMessage{Event: messageevent.ReturnRoute, Clients: []models.Client{client}, Time: event.OccurredAt, Message: title + "\n" + message}); err == nil {
		now := time.Now().UTC()
		_ = db.Model(&models.ReturnRouteStatus{}).Where("task_id = ?", task.Id).Update("last_notified_at", now).Error
	}
}

func formatReturnRouteNotification(task models.ReturnRouteTask, event models.ReturnRouteEvent) string {
	carrierNames := map[string]string{
		"mobile":  "中国移动",
		"telecom": "中国电信",
		"unicom":  "中国联通",
	}
	carrier := carrierNames[strings.ToLower(strings.TrimSpace(task.Carrier))]
	if carrier == "" {
		carrier = task.Carrier
	}
	expectedLine := strings.TrimSpace(event.ExpectedLine)
	if expectedLine == "" {
		expectedLine = strings.TrimSpace(task.ExpectedLine)
	}
	return fmt.Sprintf("任务：%s\n运营商/地区：%s / %s\n探测目标：%s\n预期线路：%s\n线路变化：%s -> %s\n识别置信度：%.0f%%\n关键 ASN：%s",
		task.Name, carrier, task.Region, task.Target, expectedLine, event.FromLine, event.ToLine, event.Confidence*100, strings.Join(event.ASNPath, " -> "))
}

func classifyReturnRoute(path models.StringArray) (string, float64) {
	asns := make([]int, 0, len(path))
	set := make(map[int]bool, len(path))
	for _, value := range path {
		asn, _ := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(value), "AS"))
		if asn > 0 {
			asns = append(asns, asn)
			set[asn] = true
		}
	}

	// Prefer the first premium ingress visible in the ordered path. The target
	// carrier's ordinary backbone usually appears later and must not mask an
	// injected route through another carrier.
	for _, asn := range asns {
		switch asn {
		case 58807:
			return "CMIN2", 0.98
		case 58453:
			return "CMI", 0.96
		case 23764:
			if set[4809] {
				return "CN2 GIA", 0.96
			}
		case 9929:
			return "9929", 0.98
		case 4809:
			if set[23764] {
				return "CN2 GIA", 0.96
			}
			return "CN2 GT", 0.88
		}
	}

	for _, asn := range asns {
		switch asn {
		case 4134, 4812:
			return "163", 0.92
		case 4837:
			return "4837", 0.96
		case 9808, 56040, 56041, 56046:
			return "CMNET", 0.90
		}
	}
	return "UNKNOWN", 0
}

type asnCacheEntry struct {
	asn     int
	expires time.Time
}

var asnCache = struct {
	sync.RWMutex
	values map[string]asnCacheEntry
}{values: map[string]asnCacheEntry{}}

func lookupASNs(ips []string) map[string]int {
	unique := map[string]struct{}{}
	for _, ip := range ips {
		unique[ip] = struct{}{}
	}
	result := make(map[string]int, len(unique))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for ip := range unique {
		ip := ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			asn := lookupASN(ctx, ip)
			mu.Lock()
			result[ip] = asn
			mu.Unlock()
		}()
	}
	wg.Wait()
	return result
}

func lookupASN(ctx context.Context, value string) int {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return 0
	}
	key := ip.String()
	asnCache.RLock()
	cached, ok := asnCache.values[key]
	asnCache.RUnlock()
	if ok && cached.expires.After(time.Now()) {
		return cached.asn
	}
	query := cymruQueryName(ip)
	texts, err := net.DefaultResolver.LookupTXT(ctx, query)
	asn := 0
	if err == nil && len(texts) > 0 {
		fields := strings.Fields(strings.ReplaceAll(texts[0], "|", " "))
		if len(fields) > 0 {
			asn, _ = strconv.Atoi(fields[0])
		}
	}
	cacheTTL := 5 * time.Minute
	if asn > 0 {
		cacheTTL = 24 * time.Hour
	}
	asnCache.Lock()
	asnCache.values[key] = asnCacheEntry{asn: asn, expires: time.Now().Add(cacheTTL)}
	asnCache.Unlock()
	return asn
}

func cymruQueryName(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", v4[3], v4[2], v4[1], v4[0])
	}
	hex := fmt.Sprintf("%032x", ip.To16())
	chars := strings.Split(hex, "")
	for left, right := 0, len(chars)-1; left < right; left, right = left+1, right-1 {
		chars[left], chars[right] = chars[right], chars[left]
	}
	return strings.Join(chars, ".") + ".origin6.asn.cymru.com"
}

func ReloadReturnRouteSchedule() error {
	list, err := GetEnabledReturnRouteTasks()
	if err != nil {
		return err
	}
	return utils.ReloadReturnRouteSchedule(list)
}
