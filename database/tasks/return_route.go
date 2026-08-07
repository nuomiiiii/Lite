package tasks

import (
	"fmt"
	"net"
	"strconv"
	"strings"
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

const (
	returnRouteLineCUGVIP       = "CUG VIP"
	returnRouteLineCUGOptimized = "CUG 优化"
	returnRouteLineCN2Pending   = "CN2 待确认"
)

type ReturnRouteOverview struct {
	Tasks    []models.ReturnRouteTask   `json:"tasks"`
	Statuses []models.ReturnRouteStatus `json:"statuses"`
	Events   []models.ReturnRouteEvent  `json:"events"`
}

type ReturnRouteSummary struct {
	Tasks        int64 `json:"tasks"`
	Active       int64 `json:"active"`
	Healthy      int64 `json:"healthy"`
	Switched     int64 `json:"switched"`
	Abnormal     int64 `json:"abnormal"`
	RecentEvents int64 `json:"recent_events"`
}

type ReturnRouteTaskQuery struct {
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	TaskID   uint   `json:"task_id"`
	Keyword  string `json:"keyword"`
	Carrier  string `json:"carrier"`
	State    string `json:"state"`
}

type ReturnRouteTaskPage struct {
	Tasks          []models.ReturnRouteTask   `json:"tasks"`
	Statuses       []models.ReturnRouteStatus `json:"statuses"`
	ProbingTaskIDs []uint                     `json:"probing_task_ids"`
	Total          int64                      `json:"total"`
	Page           int                        `json:"page"`
	PageSize       int                        `json:"page_size"`
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
	task.ExpectedLine = normalizeReturnRouteLine(task.ExpectedLine)
	task.Protocol = strings.ToLower(strings.TrimSpace(task.Protocol))
	if task.Name == "" || task.Client == "" || task.Target == "" || task.ExpectedLine == "" {
		return fmt.Errorf("任务名称、客户端、探测目标和预期线路为必填项")
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
	return []string{"CMIN2", "CMI", "CMNET", "CN2 GIA", "CN2 GT", "163", returnRouteLineCUGVIP, returnRouteLineCUGOptimized, "9929", "4837"}
}

func normalizeReturnRouteLine(value string) string {
	line := strings.ToUpper(strings.TrimSpace(value))
	if line == "10099" {
		return returnRouteLineCUGVIP
	}
	return line
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
		"notify": task.Notify, "notify_recovery": task.NotifyRecovery, "enabled": task.Enabled,
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

func GetReturnRouteSummary() (ReturnRouteSummary, error) {
	return getReturnRouteSummary(dbcore.GetDBInstance(), time.Now().UTC())
}

func getReturnRouteSummary(db *gorm.DB, now time.Time) (ReturnRouteSummary, error) {
	var result ReturnRouteSummary
	if err := db.Model(&models.ReturnRouteTask{}).Count(&result.Tasks).Error; err != nil {
		return result, err
	}
	if err := db.Model(&models.ReturnRouteTask{}).Where("enabled = ?", true).Count(&result.Active).Error; err != nil {
		return result, err
	}
	activeTasks := db.Model(&models.ReturnRouteTask{}).Select("id").Where("enabled = ?", true)
	if err := db.Model(&models.ReturnRouteStatus{}).Where("task_id IN (?) AND state = ? AND (last_error IS NULL OR last_error = ?)", activeTasks, "healthy", "").Count(&result.Healthy).Error; err != nil {
		return result, err
	}
	activeTasks = db.Model(&models.ReturnRouteTask{}).Select("id").Where("enabled = ?", true)
	if err := db.Model(&models.ReturnRouteStatus{}).Where("task_id IN (?) AND state = ? AND (last_error IS NULL OR last_error = ?)", activeTasks, "switched", "").Count(&result.Switched).Error; err != nil {
		return result, err
	}
	activeTasks = db.Model(&models.ReturnRouteTask{}).Select("id").Where("enabled = ?", true)
	if err := db.Model(&models.ReturnRouteStatus{}).Where("task_id IN (?) AND (state = ? OR (last_error IS NOT NULL AND last_error <> ?))", activeTasks, "unknown", "").Count(&result.Abnormal).Error; err != nil {
		return result, err
	}
	if err := db.Model(&models.ReturnRouteEvent{}).Where("occurred_at >= ?", now.Add(-24*time.Hour)).Count(&result.RecentEvents).Error; err != nil {
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
	result := ReturnRouteTaskPage{Tasks: []models.ReturnRouteTask{}, Statuses: []models.ReturnRouteStatus{}, ProbingTaskIDs: []uint{}, Page: page, PageSize: pageSize}
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
		pageTasks := make(map[uint]bool, len(ids))
		for _, id := range ids {
			pageTasks[id] = true
		}
		for _, id := range utils.ProbingReturnRouteTaskIDs() {
			if pageTasks[id] {
				result.ProbingTaskIDs = append(result.ProbingTaskIDs, id)
			}
		}
	}
	return result, nil
}

func filterReturnRouteTasks(query *gorm.DB, params ReturnRouteTaskQuery, db *gorm.DB) (*gorm.DB, error) {
	if params.TaskID > 0 {
		query = query.Where("id = ?", params.TaskID)
	}
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
		if probing := utils.ProbingReturnRouteTaskIDs(); len(probing) > 0 {
			query = query.Where("id NOT IN ?", probing)
		}
	case "probing":
		probing := utils.ProbingReturnRouteTaskIDs()
		if len(probing) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("enabled = ? AND id IN ?", true, probing)
		}
	case "observing", "healthy", "switched", "unknown":
		statuses := db.Model(&models.ReturnRouteStatus{}).Select("task_id").Where("state = ?", state)
		query = query.Where("id IN (?)", statuses)
		if probing := utils.ProbingReturnRouteTaskIDs(); len(probing) > 0 {
			query = query.Where("id NOT IN ?", probing)
		}
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
	if line := normalizeReturnRouteLine(params.ActualLine); line != "" {
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
		query = query.Where("(carrier = ? OR ((carrier = '' OR carrier IS NULL) AND task_id IN (?)))", carrier, tasks)
	}
	if region := strings.TrimSpace(params.Region); region != "" {
		tasks := db.Model(&models.ReturnRouteTask{}).Select("id").Where("region = ?", region)
		query = query.Where("(region = ? OR ((region = '' OR region IS NULL) AND task_id IN (?)))", region, tasks)
	}
	if line := normalizeReturnRouteLine(params.ExpectedLine); line != "" {
		if !isReturnRouteLine(line) {
			return query, fmt.Errorf("unsupported expected_line %q", line)
		}
		tasks := db.Model(&models.ReturnRouteTask{}).Select("id").Where("expected_line = ?", line)
		query = query.Where("(expected_line = ? OR ((expected_line = '' OR expected_line IS NULL) AND task_id IN (?)))", line, tasks)
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
	value = normalizeReturnRouteLine(value)
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
	defer utils.FinishReturnRouteProbe(task.Id)
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
		if isPublicReturnRouteIP(ip) {
			publicIPs = append(publicIPs, ip)
		}
	}
	rules := currentReturnRouteRules()
	asns := lookupASNsWithRules(publicIPs, rules)
	asnPath := make(models.StringArray, 0, len(publicIPs))
	seen := map[int]bool{}
	for _, ip := range publicIPs {
		asn := asns[ip]
		if asn > 0 && !seen[asn] {
			asnPath = append(asnPath, fmt.Sprintf("AS%d", asn))
			seen[asn] = true
		}
	}
	hops := make([]returnRouteSignature, 0, len(result.Hops))
	for _, hop := range result.Hops {
		ip := strings.TrimSpace(hop.IP)
		if hop.Timeout || ip == "" {
			hops = append(hops, returnRouteSignature{hidden: true})
			continue
		}
		if isPublicReturnRouteIP(ip) {
			hops = append(hops, returnRouteSignature{ip: ip, asn: asns[ip]})
		}
	}
	line, confidence := classifyReturnRouteSignaturesWithRules(hops, rules)
	probeError := strings.TrimSpace(result.Error)
	if probeError == "" && len(publicIPs) == 0 {
		probeError = "no route hops were returned"
	}
	if probeError == "" && line == "UNKNOWN" {
		probeError = "route collected, but no carrier ASN was identified"
	}

	var event *models.ReturnRouteEvent
	var statusSnapshot models.ReturnRouteStatus
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
			event = applyReturnRouteObservation(&status, task, line, now)
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
		statusSnapshot = status
		return tx.Where("occurred_at < ?", now.Add(-returnRouteEventRetention)).Delete(&models.ReturnRouteEvent{}).Error
	})
	if err != nil {
		return err
	}
	if event != nil {
		if shouldSendReturnRouteEventNotification(task, *event) {
			go sendReturnRouteNotification(task, *event, false)
		}
	} else if shouldSendReturnRouteRepeatNotificationAfterObservation(task, statusSnapshot, line, now) {
		if reminder := buildReturnRouteRepeatNotification(task, statusSnapshot, now); reminder != nil {
			go sendReturnRouteNotification(task, *reminder, true)
		}
	}
	return nil
}

func shouldSendReturnRouteEventNotification(task models.ReturnRouteTask, event models.ReturnRouteEvent) bool {
	switch event.Kind {
	case "switch":
		return task.Notify
	case "recovery":
		return task.NotifyRecovery
	default:
		return false
	}
}

func shouldSendReturnRouteRepeatNotification(task models.ReturnRouteTask, status models.ReturnRouteStatus, now time.Time) bool {
	if !task.Notify || status.State != "switched" || strings.TrimSpace(status.CurrentLine) == "" {
		return false
	}
	return returnRouteRepeatNotificationDue(status.LastNotifiedAt, task.Cooldown, now)
}

func shouldSendReturnRouteRepeatNotificationAfterObservation(task models.ReturnRouteTask, status models.ReturnRouteStatus, line string, now time.Time) bool {
	return line != returnRouteLineCN2Pending && shouldSendReturnRouteRepeatNotification(task, status, now)
}

func returnRouteRepeatNotificationDue(lastNotifiedAt *time.Time, cooldown int, now time.Time) bool {
	return lastNotifiedAt == nil || cooldown <= 0 || !now.Before(lastNotifiedAt.Add(time.Duration(cooldown)*time.Second))
}

func advanceReturnRouteState(status *models.ReturnRouteStatus, task models.ReturnRouteTask, line string, now time.Time) *models.ReturnRouteEvent {
	expected := normalizeReturnRouteLine(task.ExpectedLine)
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

func applyReturnRouteObservation(status *models.ReturnRouteStatus, task models.ReturnRouteTask, line string, now time.Time) *models.ReturnRouteEvent {
	if line != returnRouteLineCN2Pending {
		return advanceReturnRouteState(status, task, line, now)
	}
	status.CandidateLine = returnRouteLineCN2Pending
	status.CandidateCount = 0
	if status.CurrentLine == "" {
		status.State = "unknown"
	}
	return nil
}

func buildReturnRouteRepeatNotification(task models.ReturnRouteTask, status models.ReturnRouteStatus, now time.Time) *models.ReturnRouteEvent {
	if status.State != "switched" || strings.TrimSpace(status.CurrentLine) == "" {
		return nil
	}
	return &models.ReturnRouteEvent{
		TaskId: task.Id, Client: task.Client, TaskName: task.Name, Carrier: task.Carrier,
		Region: task.Region, Target: task.Target, IPVersion: task.IPVersion,
		ExpectedLine: normalizeReturnRouteLine(task.ExpectedLine), Kind: "switch",
		FromLine: normalizeReturnRouteLine(task.ExpectedLine), ToLine: status.CurrentLine,
		Confidence: status.Confidence, ASNPath: append(models.StringArray{}, status.ASNPath...),
		RoutePath: append(models.StringArray{}, status.RoutePath...), OccurredAt: now,
	}
}

func sendReturnRouteNotification(task models.ReturnRouteTask, event models.ReturnRouteEvent, repeated bool) {
	db := dbcore.GetDBInstance()
	var currentTask models.ReturnRouteTask
	if err := db.Select("notify", "notify_recovery", "cooldown").First(&currentTask, task.Id).Error; err != nil {
		return
	}
	if repeated {
		if !currentTask.Notify {
			return
		}
	} else if !shouldSendReturnRouteEventNotification(currentTask, event) {
		return
	}
	now := time.Now().UTC()
	if repeated {
		var status models.ReturnRouteStatus
		if err := db.First(&status, "task_id = ?", task.Id).Error; err != nil || !returnRouteRepeatNotificationDue(status.LastNotifiedAt, currentTask.Cooldown, now) {
			return
		}
	}
	title := "回程线路已切换"
	if repeated {
		title = "回程线路仍处于切线状态"
	} else if event.Kind == "recovery" {
		title = "回程线路已恢复"
	}
	client := task.ClientInfo
	if client.UUID == "" {
		client.UUID = task.Client
	}
	message := formatReturnRouteNotification(task, event)
	if err := messageSender.SendEvent(models.EventMessage{Event: messageevent.ReturnRoute, Clients: []models.Client{client}, Time: event.OccurredAt, Message: title + "\n" + message}); err == nil {
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
	hops := make([]returnRouteSignature, 0, len(path))
	for _, value := range path {
		asn, _ := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(value), "AS"))
		if asn > 0 {
			hops = append(hops, returnRouteSignature{asn: asn})
		}
	}
	return classifyReturnRouteSignatures(hops)
}

type returnRouteSignature struct {
	ip     string
	asn    int
	hidden bool
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

func classifyReturnRouteSignaturesWithRules(hops []returnRouteSignature, rules *compiledReturnRouteRules) (string, float64) {
	hops, hiddenHops := prepareReturnRouteSignatures(hops)
	hasCUGAccess := hasUnicomReturnRouteGroup(hops, rules, "unicom_10099")
	has9929 := hasUnicomReturnRouteGroup(hops, rules, "unicom_9929")
	has4837 := hasUnicomReturnRouteGroup(hops, rules, "unicom_4837")

	// Prefer the first premium ingress visible in the ordered path. The target
	// carrier's ordinary backbone usually appears later and must not mask an
	// injected route through another carrier.
	for _, hop := range hops {
		switch unicomReturnRouteGroup(hop, rules) {
		case "unicom_10099":
			switch {
			case has9929:
				return returnRouteLineCUGVIP, lowerReturnRouteConfidence(
					rules.document.Confidence["unicom_10099"],
					rules.document.Confidence["unicom_9929"],
				)
			case has4837:
				return returnRouteLineCUGOptimized, lowerReturnRouteConfidence(
					rules.document.Confidence["unicom_10099"],
					rules.document.Confidence["unicom_4837"],
				)
			default:
				// Some traceroutes stop at the CUG access network. Keep the former
				// AS10099 behavior compatible by treating that incomplete path as VIP.
				return returnRouteLineCUGVIP, rules.document.Confidence["unicom_10099"]
			}
		case "unicom_9929":
			if hasCUGAccess {
				return returnRouteLineCUGVIP, lowerReturnRouteConfidence(
					rules.document.Confidence["unicom_10099"],
					rules.document.Confidence["unicom_9929"],
				)
			}
			return "9929", rules.document.Confidence["unicom_9929"]
		}
		switch {
		case rules.hasSignature("cmin2", hop):
			return "CMIN2", rules.document.Confidence["cmin2"]
		case rules.hasSignature("cmi", hop):
			return "CMI", rules.document.Confidence["cmi"]
		}
	}
	if line, confidence, ok := classifyCN2ReturnRoute(hops, hiddenHops, rules); ok {
		return line, confidence
	}

	for _, hop := range hops {
		switch {
		case rules.hasSignature("telecom_163", hop):
			return "163", rules.document.Confidence["telecom_163"]
		case unicomReturnRouteGroup(hop, rules) == "unicom_4837":
			return "4837", rules.document.Confidence["unicom_4837"]
		case rules.hasSignature("cmnet", hop):
			return "CMNET", rules.document.Confidence["cmnet"]
		}
		if rules.hasPrefix("telecom_163", hop.ip) {
			return "163", rules.document.Confidence["telecom_163_prefix"]
		}
	}
	return "UNKNOWN", 0
}

func prepareReturnRouteSignatures(hops []returnRouteSignature) ([]returnRouteSignature, int) {
	prepared := make([]returnRouteSignature, 0, len(hops))
	hidden := 0
	for _, hop := range hops {
		hop.ip = strings.TrimSpace(hop.ip)
		if hop.hidden || hop.ip == "*" {
			hidden++
			continue
		}
		if hop.ip != "" && !isPublicReturnRouteIP(hop.ip) {
			continue
		}
		if hop.ip == "" && hop.asn <= 0 {
			continue
		}
		prepared = append(prepared, hop)
	}
	return prepared, hidden
}

func isPublicReturnRouteIP(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

func classifyCN2ReturnRoute(hops []returnRouteSignature, hiddenHops int, rules *compiledReturnRouteRules) (string, float64, bool) {
	pendingConfidence := 0.5
	if hiddenHops >= 3 {
		pendingConfidence = 0.4
	}

	firstCN2 := -1
	for index, hop := range hops {
		if rules.hasSignature("cn2_backbone", hop) {
			firstCN2 = index
			break
		}
	}
	if firstCN2 < 0 {
		return "", 0, false
	}

	for index := 0; index < firstCN2; index++ {
		if rules.hasSignature("cn2_global", hops[index]) {
			return "CN2 GIA", rules.document.Confidence["cn2_gia"], true
		}
	}
	cn2Count := 0
	firstTelecomAfterCN2 := -1
	telecomTransitCount := 0
	for index := firstCN2; index < len(hops); index++ {
		hop := hops[index]
		if rules.hasSignature("cn2_backbone", hop) {
			cn2Count++
		}
		if index > firstCN2 && rules.hasSignature("telecom_163", hop) {
			if firstTelecomAfterCN2 < 0 {
				firstTelecomAfterCN2 = index
			}
			if index < len(hops)-1 {
				telecomTransitCount++
			}
		}
	}

	if telecomTransitCount >= 2 && !hasCN2BackboneAfter(hops, firstTelecomAfterCN2, rules) {
		return "CN2 GT", rules.document.Confidence["cn2_gt_strong"], true
	}
	if firstTelecomAfterCN2 >= 0 {
		// A single final AS4134 hop is the target access network, not a
		// domestic 163 transit segment. Require multiple CN2 hops so an
		// incomplete one-hop observation still remains pending.
		if firstTelecomAfterCN2 == len(hops)-1 && cn2Count >= 2 {
			return "CN2 GIA", rules.document.Confidence["cn2_gia"], true
		}
		return returnRouteLineCN2Pending, pendingConfidence, true
	}
	if cn2Count >= 2 {
		return "CN2 GIA", rules.document.Confidence["cn2_gia"], true
	}
	return returnRouteLineCN2Pending, pendingConfidence, true
}

func hasUnicomReturnRouteGroup(hops []returnRouteSignature, rules *compiledReturnRouteRules, group string) bool {
	for _, hop := range hops {
		if unicomReturnRouteGroup(hop, rules) == group {
			return true
		}
	}
	return false
}

func unicomReturnRouteGroup(hop returnRouteSignature, rules *compiledReturnRouteRules) string {
	groups := [...]string{"unicom_10099", "unicom_9929", "unicom_4837"}
	// A maintained CIDR is more reliable than a conflicting external ASN answer.
	for _, group := range groups {
		if rules.hasPrefix(group, hop.ip) {
			return group
		}
	}
	for _, group := range groups {
		if rules.hasASN(group, hop.asn) {
			return group
		}
	}
	return ""
}

func lowerReturnRouteConfidence(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func hasASNGroupBefore(hops []returnRouteSignature, index int, rules *compiledReturnRouteRules, group string) bool {
	for i := 0; i < index; i++ {
		if rules.hasSignature(group, hops[i]) {
			return true
		}
	}
	return false
}

func hasCN2BackboneAfter(hops []returnRouteSignature, index int, rules *compiledReturnRouteRules) bool {
	for i := index + 1; i < len(hops); i++ {
		if rules.hasSignature("cn2_backbone", hops[i]) {
			return true
		}
	}
	return false
}

func ReloadReturnRouteSchedule() error {
	list, err := GetEnabledReturnRouteTasks()
	if err != nil {
		return err
	}
	return utils.ReloadReturnRouteSchedule(list)
}
