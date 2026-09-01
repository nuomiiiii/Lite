package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	messageevent "github.com/nuomiiiii/lite/database/models/messageEvent"
	"github.com/nuomiiiii/lite/utils"
	"github.com/nuomiiiii/lite/utils/messageSender"
	"gorm.io/gorm"
)

const (
	mainlandOutcomeReachable     = "reachable"
	mainlandOutcomeTruncated     = "route_truncated"
	mainlandOutcomeIndeterminate = "indeterminate"
	mainlandOutcomeInvalid       = "invalid"

	mainlandStateNormal           = "normal"
	mainlandStateObserving        = "observing"
	mainlandStateSuspectedBlocked = "suspected_blocked"
	mainlandStateRecovering       = "recovering"
	mainlandStateUndetermined     = "undetermined"

	mainlandDisplayNormal           = "normal"
	mainlandDisplaySingleCarrier    = "single_carrier"
	mainlandDisplayInsufficient     = "insufficient"
	mainlandDisplayCollecting       = "collecting"
	mainlandDisplayUndetermined     = "undetermined"
	mainlandDisplaySuspectedBlocked = "suspected_blocked"

	mainlandEventBlocked  = "mainland_blocked"
	mainlandEventRepeat   = "mainland_repeat"
	mainlandEventRecovery = "mainland_recovery"

	mainlandLineBlocked   = "suspected_blocked"
	mainlandLineReachable = "mainland_reachable"

	mainlandSampleRetention   = 24 * time.Hour
	mainlandMinWindow         = 5 * time.Minute
	mainlandFailRateThreshold = 0.90
	mainlandMinValidSamples   = 2
)

type mainlandCarrierStat struct {
	Carrier           string     `json:"carrier"`
	Valid             int        `json:"valid"`
	Failures          int        `json:"failures"`
	FailRate          float64    `json:"fail_rate"`
	Abnormal          bool       `json:"abnormal"`
	HasBaseline       bool       `json:"has_baseline"`
	LatestValidAt     *time.Time `json:"latest_valid_at,omitempty"`
	LastLine          string     `json:"last_line,omitempty"`
	LastNote          string     `json:"last_note,omitempty"`
	WindowSeconds     int        `json:"window_seconds"`
	Consecutive       int        `json:"consecutive"`
	Recovered         bool       `json:"recovered"`
	PathCandidate     bool       `json:"path_candidate"`
	PingReady         bool       `json:"ping_ready"`
	PingTaskID        uint       `json:"ping_task_id,omitempty"`
	PingTaskName      string     `json:"ping_task_name,omitempty"`
	PingType          string     `json:"ping_type,omitempty"`
	PingTarget        string     `json:"ping_target,omitempty"`
	PingTotal         int64      `json:"ping_total,omitempty"`
	PingLost          int64      `json:"ping_lost,omitempty"`
	PingLossRate      float64    `json:"ping_loss_rate"`
	PingWindowSeconds int        `json:"ping_window_seconds,omitempty"`
}

type mainlandEvidenceBlob struct {
	Evidence      []mainlandCarrierStat `json:"evidence"`
	LastLines     map[string]string     `json:"last_lines,omitempty"`
	WindowSeconds int                   `json:"window_seconds"`
}

type mainlandReachabilityDetail struct {
	FailedCarriers    []string              `json:"failed_carriers,omitempty"`
	RecoveredCarriers []string              `json:"recovered_carriers,omitempty"`
	WindowSeconds     int                   `json:"window_seconds"`
	Evidence          []mainlandCarrierStat `json:"evidence,omitempty"`
	AgentOnline       bool                  `json:"agent_online"`
	HighConfidence    bool                  `json:"high_confidence"`
	LastLines         map[string]string     `json:"last_lines,omitempty"`
	AbnormalStartedAt *time.Time            `json:"abnormal_started_at,omitempty"`
	Consecutive       map[string]int        `json:"consecutive,omitempty"`
}

// ReturnRouteReachabilityView is the node-level overlay returned with task queries.
type ReturnRouteReachabilityView struct {
	Client            string                `json:"client"`
	IPVersion         int                   `json:"ip_version"`
	State             string                `json:"state"`
	Display           string                `json:"display"`
	FailedCarriers    []string              `json:"failed_carriers,omitempty"`
	HighConfidence    bool                  `json:"high_confidence"`
	AbnormalStartedAt *time.Time            `json:"abnormal_started_at,omitempty"`
	LastChangedAt     *time.Time            `json:"last_changed_at,omitempty"`
	LastLines         map[string]string     `json:"last_lines,omitempty"`
	Evidence          []mainlandCarrierStat `json:"evidence,omitempty"`
	WindowSeconds     int                   `json:"window_seconds,omitempty"`
}

type mainlandNotifyFunc func(title, body string, event models.ReturnRouteEvent, client models.Client) error

var mainlandClientOnline = utils.IsReturnRouteClientOnline

var sendMainlandReachabilityEvent mainlandNotifyFunc = defaultSendMainlandReachabilityEvent

var queryMainlandPingLoss = queryMainlandPingLossFromStore

var mainlandPingLossTestHook func(client string, taskID uint, start, end time.Time) (metricstore.PingLossStats, error)

func queryMainlandPingLossFromStore(client string, taskID uint, start, end time.Time) (metricstore.PingLossStats, error) {
	if mainlandPingLossTestHook != nil {
		return mainlandPingLossTestHook(client, taskID, start, end)
	}
	store := metricstore.GetStore()
	if store == nil {
		return metricstore.PingLossStats{}, nil
	}
	return metricstore.QueryPingLossStats(context.Background(), store, client, taskID, start, end)
}

func mainlandPingTaskID(task models.ReturnRouteTask) uint {
	if task.MainlandReachabilityPingTaskID == nil {
		return 0
	}
	return *task.MainlandReachabilityPingTaskID
}

func mainlandAssistWindow(returnInterval, pingInterval int) time.Duration {
	window := mainlandWindow(returnInterval)
	if pingInterval > 0 {
		if pingWindow := time.Duration(pingInterval) * 2 * time.Second; pingWindow > window {
			window = pingWindow
		}
	}
	return window
}

func mainlandPingAssigned(ping models.PingTask, client string) bool {
	return ping.Id > 0 && ping.AppliesToClient(client)
}

func hideInvalidMainlandPingAssociations(db *gorm.DB, tasks []models.ReturnRouteTask) error {
	ids := make([]uint, 0, len(tasks))
	seen := map[uint]bool{}
	for _, task := range tasks {
		id := mainlandPingTaskID(task)
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	var pings []models.PingTask
	if err := db.Where("id IN ?", ids).Find(&pings).Error; err != nil {
		return err
	}
	byID := make(map[uint]models.PingTask, len(pings))
	for _, ping := range pings {
		byID[ping.Id] = ping
	}
	for i := range tasks {
		id := mainlandPingTaskID(tasks[i])
		if id == 0 {
			continue
		}
		ping, ok := byID[id]
		if !ok || !mainlandPingAssigned(ping, tasks[i].Client) {
			tasks[i].MainlandReachabilityPingTaskID = nil
		}
	}
	return nil
}

func detachReturnRouteMainlandPing(tx *gorm.DB, pingIDs []uint, clients []string) ([]models.ReturnRouteTask, error) {
	if len(pingIDs) == 0 || !tx.Migrator().HasTable(&models.ReturnRouteTask{}) {
		return nil, nil
	}
	find := tx.Where("mainland_reachability_ping_task_id IN ?", pingIDs)
	if len(clients) > 0 {
		find = find.Where("client IN ?", clients)
	}
	var tasks []models.ReturnRouteTask
	if err := find.Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	upd := tx.Model(&models.ReturnRouteTask{}).Where("mainland_reachability_ping_task_id IN ?", pingIDs)
	if len(clients) > 0 {
		upd = upd.Where("client IN ?", clients)
	}
	if err := upd.Update("mainland_reachability_ping_task_id", nil).Error; err != nil {
		return nil, err
	}
	for i := range tasks {
		tasks[i].MainlandReachabilityPingTaskID = nil
	}
	return tasks, nil
}

func mainlandReachabilityCanSample(db *gorm.DB, task models.ReturnRouteTask) bool {
	if !task.MainlandReachabilityEnabled {
		return false
	}
	pingID := mainlandPingTaskID(task)
	if pingID == 0 || db == nil {
		return false
	}
	var ping models.PingTask
	if err := db.Select("id", "clients").Where("id = ?", pingID).First(&ping).Error; err != nil {
		return false
	}
	return mainlandPingAssigned(ping, task.Client)
}

func defaultSendMainlandReachabilityEvent(title, body string, event models.ReturnRouteEvent, client models.Client) error {
	if client.UUID == "" {
		client.UUID = event.Client
	}
	return messageSender.SendEvent(models.EventMessage{
		Event:   messageevent.ReturnRoute,
		Clients: []models.Client{client},
		Time:    event.OccurredAt,
		Message: title + "\n" + body,
	})
}

func mainlandWindow(interval int) time.Duration {
	window := time.Duration(interval) * 2 * time.Second
	if window < mainlandMinWindow {
		return mainlandMinWindow
	}
	return window
}

func writeMainlandProbeSample(tx *gorm.DB, task models.ReturnRouteTask, class mainlandProbeClassification, now time.Time) error {
	if !mainlandReachabilityCanSample(tx, task) {
		return nil
	}
	sample := models.ReturnRouteProbeSample{
		TaskId:          task.Id,
		Client:          task.Client,
		Carrier:         task.Carrier,
		IPVersion:       task.IPVersion,
		Outcome:         class.Outcome,
		ClassifiedLine:  class.ClassifiedLine,
		LineState:       class.LineState,
		RouteSignature:  class.Signature,
		TerminalTTL:     class.TerminalTTL,
		TerminalAnchor:  class.TerminalAnchor,
		TargetReached:   class.TargetReached,
		BaselineVersion: class.BaselineVersion,
		CheckedAt:       now,
	}
	if err := tx.Create(&sample).Error; err != nil {
		return err
	}
	return cleanupMainlandReachabilityData(tx, now)
}

func taskHasMainlandBaseline(status models.ReturnRouteStatus) bool {
	return status.BaselineReady && strings.TrimSpace(status.BaselineLine) != "" && status.BaselineLine == strings.TrimSpace(status.CurrentLine)
}

func loadMainlandParticipatingTasks(db *gorm.DB, client string, ipVersion int) ([]models.ReturnRouteTask, error) {
	var tasks []models.ReturnRouteTask
	err := db.Preload("ClientInfo").
		Where("client = ? AND ip_version = ? AND enabled = ? AND mainland_reachability_enabled = ?", client, ipVersion, true, true).
		Order("id ASC").
		Find(&tasks).Error
	return tasks, err
}

func evaluateMainlandReachability(db *gorm.DB, client string, ipVersion int, trigger models.ReturnRouteTask, now time.Time) error {
	if strings.TrimSpace(client) == "" || (ipVersion != 4 && ipVersion != 6) {
		return nil
	}
	participating, err := loadMainlandParticipatingTasks(db, client, ipVersion)
	if err != nil {
		return err
	}
	var existing models.ReturnRouteReachabilityStatus
	found := false
	if err := db.Where("client = ? AND ip_version = ?", client, ipVersion).First(&existing).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			return err
		}
	} else {
		found = true
	}

	if len(participating) == 0 {
		if found {
			return db.Delete(&existing).Error
		}
		return nil
	}

	if trigger.Id == 0 {
		trigger = participating[0]
	}

	online := mainlandClientOnline(client)
	prevState := existing.State
	if prevState == "" {
		prevState = mainlandStateNormal
	}

	stats, lastLines, maxWindow, err := computeMainlandCarrierStats(db, participating, now)
	if err != nil {
		return err
	}

	carriers := uniqueMainlandCarriers(participating)
	baselineCarriers := 0
	abnormalCarriers := make([]string, 0, len(stats))
	recoveredCarriers := make([]string, 0, len(stats))
	missingBaseline := false
	for _, carrier := range carriers {
		stat, ok := stats[carrier]
		if !ok || !stat.HasBaseline {
			missingBaseline = true
			continue
		}
		baselineCarriers++
		if stat.Abnormal {
			abnormalCarriers = append(abnormalCarriers, carrier)
		}
		if stat.Recovered {
			recoveredCarriers = append(recoveredCarriers, carrier)
		}
	}
	sort.Strings(abnormalCarriers)
	sort.Strings(recoveredCarriers)

	simultaneous := mainlandAbnormalSimultaneous(stats, abnormalCarriers, now)
	blockedMatch := online && len(abnormalCarriers) >= 2 && simultaneous
	recovered := len(recoveredCarriers) >= 2

	nextState := prevState
	display := mainlandDisplayNormal
	notifyKind := ""
	highConfidence := len(abnormalCarriers) >= 3
	abnormalStarted := existing.AbnormalStartedAt

	switch {
	case !online:
		display = mainlandDisplayUndetermined
		if prevState == "" {
			nextState = mainlandStateUndetermined
		}
	case len(carriers) < 2:
		display = mainlandDisplayInsufficient
		if prevState == mainlandStateSuspectedBlocked || prevState == mainlandStateRecovering || prevState == mainlandStateObserving {
			nextState = mainlandStateNormal
			abnormalStarted = nil
		}
	case blockedMatch:
		highConfidence = len(abnormalCarriers) >= 3
		switch prevState {
		case mainlandStateObserving:
			nextState = mainlandStateSuspectedBlocked
			display = mainlandDisplaySuspectedBlocked
			notifyKind = mainlandEventBlocked
			if abnormalStarted == nil {
				abnormalStarted = &now
			}
		case mainlandStateSuspectedBlocked, mainlandStateRecovering:
			nextState = mainlandStateSuspectedBlocked
			display = mainlandDisplaySuspectedBlocked
			if shouldSendMainlandRepeat(existing.LastNotifiedAt, participating, now) {
				notifyKind = mainlandEventRepeat
			}
		default:
			nextState = mainlandStateObserving
			display = mainlandDisplayNormal
			if abnormalStarted == nil {
				abnormalStarted = &now
			}
		}
	default:
		if prevState == mainlandStateSuspectedBlocked || prevState == mainlandStateRecovering {
			if recovered && online {
				nextState = mainlandStateNormal
				display = mainlandDisplayNormal
				notifyKind = mainlandEventRecovery
				abnormalStarted = existing.AbnormalStartedAt
			} else {
				nextState = mainlandStateRecovering
				display = mainlandDisplaySuspectedBlocked
			}
		} else if prevState == mainlandStateObserving {
			nextState = mainlandStateNormal
			abnormalStarted = nil
			if len(abnormalCarriers) == 1 {
				display = mainlandDisplaySingleCarrier
			} else if missingBaseline && baselineCarriers < 2 {
				display = mainlandDisplayCollecting
			} else {
				display = mainlandDisplayNormal
			}
		} else if len(abnormalCarriers) == 1 {
			display = mainlandDisplaySingleCarrier
			nextState = mainlandStateNormal
		} else if missingBaseline && baselineCarriers < 2 {
			display = mainlandDisplayCollecting
		} else {
			display = mainlandDisplayNormal
			if nextState == mainlandStateUndetermined {
				nextState = mainlandStateNormal
			}
		}
	}

	if !online {
		notifyKind = ""
	}

	changed := !found || existing.State != nextState || existing.Display != display
	if (nextState == mainlandStateNormal || nextState == mainlandStateUndetermined) && notifyKind != mainlandEventRecovery {
		if nextState == mainlandStateNormal && display != mainlandDisplaySuspectedBlocked {
			abnormalStarted = nil
		}
	}
	if notifyKind == mainlandEventRecovery {
		abnormalStarted = existing.AbnormalStartedAt
	}

	evidenceList := make([]mainlandCarrierStat, 0, len(stats))
	for _, carrier := range carriers {
		if stat, ok := stats[carrier]; ok {
			evidenceList = append(evidenceList, stat)
		}
	}
	blob, _ := json.Marshal(mainlandEvidenceBlob{
		Evidence:      evidenceList,
		LastLines:     lastLines,
		WindowSeconds: int(maxWindow.Seconds()),
	})

	row := existing
	row.Client = client
	row.IPVersion = ipVersion
	row.State = nextState
	row.Display = display
	row.FailedCarriers = models.StringArray(abnormalCarriers)
	row.CarrierEvidence = string(blob)
	row.HighConfidence = highConfidence && display == mainlandDisplaySuspectedBlocked
	row.AbnormalStartedAt = abnormalStarted
	row.UpdatedAt = now
	if changed {
		row.LastChangedAt = &now
	}

	if !found {
		if err := db.Create(&row).Error; err != nil {
			return err
		}
	} else if err := db.Save(&row).Error; err != nil {
		return err
	}

	if notifyKind == "" {
		return nil
	}

	event, title, body := buildMainlandReachabilityEvent(trigger, participating, row, stats, lastLines, abnormalCarriers, recoveredCarriers, online, maxWindow, notifyKind, now)
	if event == nil {
		return nil
	}
	if err := db.Create(event).Error; err != nil {
		return err
	}

	shouldNotify := false
	switch notifyKind {
	case mainlandEventBlocked, mainlandEventRepeat:
		shouldNotify = anyMainlandNotifyEnabled(participating, false)
	case mainlandEventRecovery:
		shouldNotify = anyMainlandNotifyEnabled(participating, true)
	}
	if !shouldNotify {
		return nil
	}

	clientInfo := trigger.ClientInfo
	if clientInfo.UUID == "" {
		clientInfo.UUID = client
		for _, task := range participating {
			if task.ClientInfo.UUID != "" {
				clientInfo = task.ClientInfo
				break
			}
		}
	}
	if err := sendMainlandReachabilityEvent(title, body, *event, clientInfo); err != nil {
		return err
	}
	return db.Model(&models.ReturnRouteReachabilityStatus{}).
		Where("id = ?", row.Id).
		Update("last_notified_at", now).Error
}

func computeMainlandCarrierStats(db *gorm.DB, participating []models.ReturnRouteTask, now time.Time) (map[string]mainlandCarrierStat, map[string]string, time.Duration, error) {
	stats := make(map[string]mainlandCarrierStat, len(participating))
	lastLines := map[string]string{}
	maxWindow := mainlandMinWindow
	taskByID := make(map[uint]models.ReturnRouteTask, len(participating))
	taskIDs := make([]uint, 0, len(participating))
	statuses := map[uint]models.ReturnRouteStatus{}
	pingIDs := make([]uint, 0, len(participating))
	seenPing := map[uint]bool{}

	ids := make([]uint, 0, len(participating))
	for _, task := range participating {
		taskByID[task.Id] = task
		taskIDs = append(taskIDs, task.Id)
		ids = append(ids, task.Id)
		if pingID := mainlandPingTaskID(task); pingID > 0 && !seenPing[pingID] {
			seenPing[pingID] = true
			pingIDs = append(pingIDs, pingID)
		}
	}

	pingByID := map[uint]models.PingTask{}
	if len(pingIDs) > 0 {
		var pingRows []models.PingTask
		if err := db.Where("id IN ?", pingIDs).Find(&pingRows).Error; err != nil {
			return nil, nil, maxWindow, err
		}
		for _, ping := range pingRows {
			pingByID[ping.Id] = ping
		}
	}

	for _, task := range participating {
		ping := pingByID[mainlandPingTaskID(task)]
		pingInterval := 0
		if mainlandPingAssigned(ping, task.Client) {
			pingInterval = ping.Interval
		}
		if window := mainlandAssistWindow(task.Interval, pingInterval); window > maxWindow {
			maxWindow = window
		}
	}

	var statusRows []models.ReturnRouteStatus
	if err := db.Where("task_id IN ?", ids).Find(&statusRows).Error; err != nil {
		return nil, nil, maxWindow, err
	}
	for _, status := range statusRows {
		statuses[status.TaskId] = status
		if line := strings.TrimSpace(status.CurrentLine); line != "" {
			task := taskByID[status.TaskId]
			if lastLines[task.Carrier] == "" {
				lastLines[task.Carrier] = line
			}
		}
	}

	var samples []models.ReturnRouteProbeSample
	if err := db.Where("task_id IN ? AND checked_at >= ?", taskIDs, now.Add(-mainlandSampleRetention)).
		Order("checked_at DESC").
		Find(&samples).Error; err != nil {
		return nil, nil, maxWindow, err
	}

	samplesByTask := make(map[uint][]models.ReturnRouteProbeSample, len(participating))
	for _, sample := range samples {
		samplesByTask[sample.TaskId] = append(samplesByTask[sample.TaskId], sample)
	}

	type pingEvidence struct {
		task     models.PingTask
		stats    metricstore.PingLossStats
		window   time.Duration
		abnormal bool
		queried  bool
	}
	type merged struct {
		valid        int
		failures     int
		hasBaseline  bool
		latestValid  *time.Time
		lastLine     string
		lastNote     string
		window       time.Duration
		recoveryNeed int
		consecutive  int
		anyRecovered bool
		pings        map[uint]*pingEvidence
	}
	mergedByCarrier := map[string]*merged{}
	pingCache := map[string]metricstore.PingLossStats{}

	lookupPing := func(client string, ping models.PingTask, window time.Duration) metricstore.PingLossStats {
		key := fmt.Sprintf("%s/%d/%d", client, ping.Id, int(window.Seconds()))
		if cached, ok := pingCache[key]; ok {
			return cached
		}
		stats, err := queryMainlandPingLoss(client, ping.Id, now.Add(-window), now)
		if err != nil {
			stats = metricstore.PingLossStats{}
		}
		pingCache[key] = stats
		return stats
	}

	for _, task := range participating {
		ping := pingByID[mainlandPingTaskID(task)]
		pingInterval := 0
		pingReady := mainlandPingAssigned(ping, task.Client)
		if pingReady {
			pingInterval = ping.Interval
		}
		window := mainlandAssistWindow(task.Interval, pingInterval)
		status := statuses[task.Id]
		hasBaseline := taskHasMainlandBaseline(status)
		carrier := task.Carrier
		item := mergedByCarrier[carrier]
		if item == nil {
			item = &merged{window: window, recoveryNeed: task.RecoveryConfirm, pings: map[uint]*pingEvidence{}}
			mergedByCarrier[carrier] = item
		}
		if window > item.window {
			item.window = window
		}
		if task.RecoveryConfirm > item.recoveryNeed {
			item.recoveryNeed = task.RecoveryConfirm
		}
		if hasBaseline {
			item.hasBaseline = true
		}
		if pingReady {
			evidence := item.pings[ping.Id]
			if evidence == nil {
				loss := lookupPing(task.Client, ping, window)
				abnormal := loss.Total >= int64(mainlandMinValidSamples) && loss.LossRatio() >= mainlandFailRateThreshold
				evidence = &pingEvidence{task: ping, stats: loss, window: window, abnormal: abnormal, queried: true}
				item.pings[ping.Id] = evidence
			} else if window > evidence.window {
				loss := lookupPing(task.Client, ping, window)
				evidence.stats = loss
				evidence.window = window
				evidence.abnormal = loss.Total >= int64(mainlandMinValidSamples) && loss.LossRatio() >= mainlandFailRateThreshold
			}
		}

		taskSamples := samplesByTask[task.Id]
		consecutive := consecutiveMainlandReachable(taskSamples)
		if consecutive > item.consecutive {
			item.consecutive = consecutive
		}
		if consecutive >= task.RecoveryConfirm {
			item.anyRecovered = true
		}

		cutoff := now.Add(-window)
		for _, sample := range taskSamples {
			if sample.CheckedAt.Before(cutoff) {
				continue
			}
			if sample.BaselineVersion != status.BaselineVersion {
				continue
			}
			if sample.Outcome == mainlandOutcomeInvalid || sample.Outcome == mainlandOutcomeIndeterminate {
				continue
			}
			if sample.LineState == mainlandLineStateSwitching || sample.LineState == mainlandLineStateRebasing {
				continue
			}
			if sample.Outcome != mainlandOutcomeReachable && sample.Outcome != mainlandOutcomeTruncated {
				continue
			}
			item.valid++
			if sample.Outcome == mainlandOutcomeTruncated {
				item.failures++
				if item.lastNote == "" && strings.TrimSpace(sample.TerminalAnchor) != "" {
					item.lastNote = sample.TerminalAnchor
				}
			}
			if sample.ClassifiedLine != "" && item.lastLine == "" {
				item.lastLine = sample.ClassifiedLine
			}
			if item.latestValid == nil || sample.CheckedAt.After(*item.latestValid) {
				checked := sample.CheckedAt
				item.latestValid = &checked
			}
		}
		if line := strings.TrimSpace(status.CurrentLine); line != "" && item.lastLine == "" {
			item.lastLine = line
		}
	}

	for carrier, item := range mergedByCarrier {
		failRate := 0.0
		if item.valid > 0 {
			failRate = float64(item.failures) / float64(item.valid)
		}
		pathCandidate := item.hasBaseline && item.valid >= mainlandMinValidSamples && failRate >= mainlandFailRateThreshold
		pingAbnormal := false
		var chosen *pingEvidence
		for _, evidence := range item.pings {
			if chosen == nil {
				chosen = evidence
			}
			if evidence.abnormal {
				pingAbnormal = true
				chosen = evidence
				break
			}
		}
		stat := mainlandCarrierStat{
			Carrier:       carrier,
			Valid:         item.valid,
			Failures:      item.failures,
			FailRate:      failRate,
			Abnormal:      pathCandidate && pingAbnormal,
			HasBaseline:   item.hasBaseline,
			LatestValidAt: item.latestValid,
			LastLine:      item.lastLine,
			LastNote:      item.lastNote,
			WindowSeconds: int(item.window.Seconds()),
			Consecutive:   item.consecutive,
			Recovered:     item.anyRecovered,
			PathCandidate: pathCandidate,
		}
		if chosen != nil {
			stat.PingReady = true
			stat.PingTaskID = chosen.task.Id
			stat.PingTaskName = chosen.task.Name
			stat.PingType = chosen.task.Type
			stat.PingTarget = chosen.task.Target
			stat.PingTotal = chosen.stats.Total
			stat.PingLost = chosen.stats.Lost
			stat.PingLossRate = chosen.stats.LossRatio()
			stat.PingWindowSeconds = int(chosen.window.Seconds())
		}
		stats[carrier] = stat
		if item.lastLine != "" {
			lastLines[carrier] = item.lastLine
		}
		if item.window > maxWindow {
			maxWindow = item.window
		}
	}
	return stats, lastLines, maxWindow, nil
}

func consecutiveMainlandReachable(samples []models.ReturnRouteProbeSample) int {
	count := 0
	for _, sample := range samples {
		if sample.LineState == mainlandLineStateSwitching || sample.LineState == mainlandLineStateRebasing {
			break
		}
		if sample.Outcome == mainlandOutcomeInvalid || sample.Outcome == mainlandOutcomeIndeterminate {
			continue
		}
		if sample.Outcome == mainlandOutcomeReachable {
			count++
			continue
		}
		break
	}
	return count
}

func uniqueMainlandCarriers(tasks []models.ReturnRouteTask) []string {
	seen := map[string]bool{}
	carriers := make([]string, 0, 3)
	for _, task := range tasks {
		if seen[task.Carrier] {
			continue
		}
		seen[task.Carrier] = true
		carriers = append(carriers, task.Carrier)
	}
	sort.Strings(carriers)
	return carriers
}

func mainlandAbnormalSimultaneous(stats map[string]mainlandCarrierStat, abnormal []string, now time.Time) bool {
	if len(abnormal) < 2 {
		return false
	}
	maxWindow := mainlandMinWindow
	for _, carrier := range abnormal {
		stat := stats[carrier]
		window := time.Duration(stat.WindowSeconds) * time.Second
		if window > maxWindow {
			maxWindow = window
		}
		if stat.LatestValidAt == nil {
			return false
		}
	}
	for _, carrier := range abnormal {
		stat := stats[carrier]
		if now.Sub(*stat.LatestValidAt) > maxWindow {
			return false
		}
	}
	return true
}

func shouldSendMainlandRepeat(lastNotifiedAt *time.Time, participating []models.ReturnRouteTask, now time.Time) bool {
	cooldown := 0
	for _, task := range participating {
		if task.Cooldown > cooldown {
			cooldown = task.Cooldown
		}
	}
	return returnRouteRepeatNotificationDue(lastNotifiedAt, cooldown, now)
}

func anyMainlandNotifyEnabled(tasks []models.ReturnRouteTask, recovery bool) bool {
	for _, task := range tasks {
		if recovery {
			if task.MainlandReachabilityRecoveryNotify {
				return true
			}
			continue
		}
		if task.MainlandReachabilityNotify {
			return true
		}
	}
	return false
}

func buildMainlandReachabilityEvent(
	trigger models.ReturnRouteTask,
	participating []models.ReturnRouteTask,
	row models.ReturnRouteReachabilityStatus,
	stats map[string]mainlandCarrierStat,
	lastLines map[string]string,
	abnormal, recovered []string,
	online bool,
	window time.Duration,
	kind string,
	now time.Time,
) (*models.ReturnRouteEvent, string, string) {
	consecutive := map[string]int{}
	evidence := make([]mainlandCarrierStat, 0, len(stats))
	for _, task := range participating {
		if _, ok := consecutive[task.Carrier]; !ok {
			if stat, exists := stats[task.Carrier]; exists {
				consecutive[task.Carrier] = stat.Consecutive
				evidence = append(evidence, stat)
			}
		}
	}
	detail := mainlandReachabilityDetail{
		FailedCarriers:    abnormal,
		RecoveredCarriers: recovered,
		WindowSeconds:     int(window.Seconds()),
		Evidence:          evidence,
		AgentOnline:       online,
		HighConfidence:    row.HighConfidence,
		LastLines:         lastLines,
		AbnormalStartedAt: row.AbnormalStartedAt,
		Consecutive:       consecutive,
	}
	raw, _ := json.Marshal(detail)
	toLine := mainlandLineBlocked
	fromLine := ""
	title := "IP 疑似被墙"
	if kind == mainlandEventRepeat {
		title = "IP 仍疑似被墙"
	}
	if kind == mainlandEventRecovery {
		title = "大陆方向可达性已恢复"
		toLine = mainlandLineReachable
		fromLine = mainlandLineBlocked
	}
	event := &models.ReturnRouteEvent{
		TaskId:       trigger.Id,
		Client:       trigger.Client,
		TaskName:     trigger.Name,
		Carrier:      "",
		Region:       trigger.Region,
		Target:       trigger.Target,
		IPVersion:    ipVersionOr(trigger.IPVersion, row.IPVersion),
		ExpectedLine: trigger.ExpectedLine,
		Kind:         kind,
		FromLine:     fromLine,
		ToLine:       toLine,
		Confidence:   1,
		Detail:       string(raw),
		OccurredAt:   now,
	}
	body := formatMainlandReachabilityNotification(trigger, participating, row, stats, lastLines, abnormal, recovered, online, window, kind, now)
	return event, title, body
}

func ipVersionOr(taskVersion, rowVersion int) int {
	if taskVersion == 4 || taskVersion == 6 {
		return taskVersion
	}
	return rowVersion
}

func formatMainlandReachabilityNotification(
	trigger models.ReturnRouteTask,
	participating []models.ReturnRouteTask,
	row models.ReturnRouteReachabilityStatus,
	stats map[string]mainlandCarrierStat,
	lastLines map[string]string,
	abnormal, recovered []string,
	online bool,
	window time.Duration,
	kind string,
	now time.Time,
) string {
	nodeName := strings.TrimSpace(trigger.ClientInfo.Name)
	if nodeName == "" {
		for _, task := range participating {
			if name := strings.TrimSpace(task.ClientInfo.Name); name != "" {
				nodeName = name
				break
			}
		}
	}
	if nodeName == "" {
		nodeName = trigger.Client
	}
	ipLabel := "IPv4"
	if trigger.IPVersion == 6 || row.IPVersion == 6 {
		ipLabel = "IPv6"
	}
	agent := "离线"
	if online {
		agent = "在线"
	}

	switch kind {
	case mainlandEventRepeat:
		started := now
		if row.AbnormalStartedAt != nil {
			started = *row.AbnormalStartedAt
		}
		return fmt.Sprintf(
			"节点：%s\n异常运营商：%s\n当前异常率：%s\nAgent 状态：%s\n已持续：%s\n最后检查：%s",
			nodeName,
			joinMainlandCarrierNames(abnormal),
			formatMainlandFailRates(abnormal, stats),
			agent,
			formatMainlandDuration(now.Sub(started)),
			formatMainlandTime(now),
		)
	case mainlandEventRecovery:
		started := now
		if row.AbnormalStartedAt != nil {
			started = *row.AbnormalStartedAt
		}
		return fmt.Sprintf(
			"节点：%s\n地址类型：%s\n恢复运营商：%s\n连续正常：%s\n当前线路：%s\n异常持续：%s\n恢复时间：%s",
			nodeName,
			ipLabel,
			joinMainlandCarrierNames(recovered),
			formatMainlandConsecutive(recovered, stats),
			formatMainlandLastLines(recovered, lastLines),
			formatMainlandDuration(now.Sub(started)),
			formatMainlandTime(now),
		)
	default:
		confidence := "疑似被墙"
		if row.HighConfidence {
			confidence = "高置信度疑似被墙（三网同时异常）"
		}
		started := now
		if row.AbnormalStartedAt != nil {
			started = *row.AbnormalStartedAt
		}
		lines := []string{
			fmt.Sprintf("节点：%s", nodeName),
			fmt.Sprintf("地址类型：%s", ipLabel),
			fmt.Sprintf("异常运营商：%s", joinMainlandCarrierNames(abnormal)),
			fmt.Sprintf("统计窗口：最近 %s", formatMainlandWindow(window)),
		}
		for _, carrier := range abnormal {
			stat := stats[carrier]
			lines = append(lines, fmt.Sprintf("%s：%d/%d 次探测异常，异常率 %.0f%%",
				returnRouteCarrierName(carrier), stat.Failures, stat.Valid, stat.FailRate*100))
		}
		lines = append(lines,
			fmt.Sprintf("Agent 状态：%s", agent),
			fmt.Sprintf("最后正常线路：%s", formatMainlandLastLines(abnormal, lastLines)),
			fmt.Sprintf("路径证据：%s", formatMainlandPathEvidence(abnormal, stats, lastLines)),
			fmt.Sprintf("辅助延迟：%s", formatMainlandPingEvidence(abnormal, stats)),
			fmt.Sprintf("置信度：%s", confidence),
			fmt.Sprintf("异常开始：%s", formatMainlandTime(started)),
			fmt.Sprintf("判定时间：%s", formatMainlandTime(now)),
		)
		return strings.Join(lines, "\n")
	}
}

func joinMainlandCarrierNames(carriers []string) string {
	names := make([]string, 0, len(carriers))
	for _, carrier := range carriers {
		names = append(names, returnRouteCarrierName(carrier))
	}
	return strings.Join(names, "、")
}

func formatMainlandFailRates(carriers []string, stats map[string]mainlandCarrierStat) string {
	parts := make([]string, 0, len(carriers))
	for _, carrier := range carriers {
		stat := stats[carrier]
		short := strings.TrimPrefix(returnRouteCarrierName(carrier), "中国")
		parts = append(parts, fmt.Sprintf("%s %.0f%%", short, stat.FailRate*100))
	}
	return strings.Join(parts, "；")
}

func formatMainlandConsecutive(carriers []string, stats map[string]mainlandCarrierStat) string {
	parts := make([]string, 0, len(carriers))
	for _, carrier := range carriers {
		stat := stats[carrier]
		short := strings.TrimPrefix(returnRouteCarrierName(carrier), "中国")
		parts = append(parts, fmt.Sprintf("%s %d 次", short, stat.Consecutive))
	}
	return strings.Join(parts, "；")
}

func formatMainlandLastLines(carriers []string, lastLines map[string]string) string {
	parts := make([]string, 0, len(carriers))
	for _, carrier := range carriers {
		line := strings.TrimSpace(lastLines[carrier])
		if line == "" {
			continue
		}
		short := strings.TrimPrefix(returnRouteCarrierName(carrier), "中国")
		parts = append(parts, short+" "+line)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "；")
}

func formatMainlandPathEvidence(carriers []string, stats map[string]mainlandCarrierStat, lastLines map[string]string) string {
	parts := make([]string, 0, len(carriers))
	for _, carrier := range carriers {
		short := strings.TrimPrefix(returnRouteCarrierName(carrier), "中国")
		line := strings.TrimSpace(lastLines[carrier])
		stat := stats[carrier]
		note := strings.TrimSpace(stat.LastNote)
		if line != "" && note != "" {
			parts = append(parts, fmt.Sprintf("%s仍识别为 %s，但在 %s 后提前截断", short, line, note))
			continue
		}
		if line != "" {
			parts = append(parts, fmt.Sprintf("%s仍识别为 %s，但未达到历史终点锚点", short, line))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s连续无法达到历史终点锚点", short))
	}
	if len(parts) == 0 {
		return "参与运营商均未达到历史终点锚点"
	}
	return strings.Join(parts, "；")
}

func formatMainlandPingEvidence(carriers []string, stats map[string]mainlandCarrierStat) string {
	parts := make([]string, 0, len(carriers))
	for _, carrier := range carriers {
		stat := stats[carrier]
		if !stat.PingReady {
			continue
		}
		short := strings.TrimPrefix(returnRouteCarrierName(carrier), "中国")
		name := strings.TrimSpace(stat.PingTaskName)
		if name == "" {
			name = "延迟监测"
		}
		kind := strings.ToUpper(strings.TrimSpace(stat.PingType))
		if kind == "" {
			kind = "ICMP"
		}
		parts = append(parts, fmt.Sprintf("%s %s %s，丢包 %d/%d（%.0f%%）",
			short, name, kind, stat.PingLost, stat.PingTotal, stat.PingLossRate*100))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "；")
}

func formatMainlandWindow(window time.Duration) string {
	minutes := int(window.Minutes())
	if minutes < 1 {
		minutes = 1
	}
	return fmt.Sprintf("%d 分钟", minutes)
}

func formatMainlandDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	}
	return fmt.Sprintf("%d 分钟", int(d.Minutes()))
}

func formatMainlandTime(value time.Time) string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.Local
	}
	return value.In(loc).Format("2006-01-02 15:04:05")
}

func reachabilityViewsForTasks(db *gorm.DB, tasks []models.ReturnRouteTask) ([]ReturnRouteReachabilityView, error) {
	if len(tasks) == 0 {
		return []ReturnRouteReachabilityView{}, nil
	}
	type key struct {
		client string
		ip     int
	}
	seen := map[key]bool{}
	clients := make([]string, 0, len(tasks))
	for _, task := range tasks {
		k := key{task.Client, task.IPVersion}
		if seen[k] {
			continue
		}
		seen[k] = true
		clients = append(clients, task.Client)
	}
	var rows []models.ReturnRouteReachabilityStatus
	if err := db.Where("client IN ?", clients).Find(&rows).Error; err != nil {
		return nil, err
	}
	wanted := seen
	views := make([]ReturnRouteReachabilityView, 0, len(rows))
	for _, row := range rows {
		if !wanted[key{row.Client, row.IPVersion}] {
			continue
		}
		views = append(views, mainlandStatusToView(row))
	}
	return views, nil
}

func mainlandStatusToView(row models.ReturnRouteReachabilityStatus) ReturnRouteReachabilityView {
	view := ReturnRouteReachabilityView{
		Client:            row.Client,
		IPVersion:         row.IPVersion,
		State:             row.State,
		Display:           row.Display,
		FailedCarriers:    []string(row.FailedCarriers),
		HighConfidence:    row.HighConfidence,
		AbnormalStartedAt: row.AbnormalStartedAt,
		LastChangedAt:     row.LastChangedAt,
	}
	if row.CarrierEvidence != "" {
		var blob mainlandEvidenceBlob
		if json.Unmarshal([]byte(row.CarrierEvidence), &blob) == nil {
			view.Evidence = blob.Evidence
			view.LastLines = blob.LastLines
			view.WindowSeconds = blob.WindowSeconds
		}
	}
	return view
}

func returnRouteReachabilityTaskIDs(db *gorm.DB, displays []string) *gorm.DB {
	return db.Model(&models.ReturnRouteReachabilityStatus{}).
		Select("return_route_tasks.id").
		Joins("JOIN return_route_tasks ON return_route_tasks.client = return_route_reachability_statuses.client AND return_route_tasks.ip_version = return_route_reachability_statuses.ip_version").
		Where("return_route_reachability_statuses.display IN ?", displays).
		Where("return_route_tasks.mainland_reachability_enabled = ?", true)
}

func blockedMainlandTaskIDs(db *gorm.DB) ([]uint, error) {
	var ids []uint
	err := returnRouteReachabilityTaskIDs(db, []string{mainlandDisplaySuspectedBlocked}).Pluck("return_route_tasks.id", &ids).Error
	return ids, err
}

func recomputeMainlandReachabilityKeys(db *gorm.DB, keys [][2]any, now time.Time) error {
	seen := map[string]bool{}
	for _, key := range keys {
		client, _ := key[0].(string)
		ipVersion, _ := key[1].(int)
		if client == "" || (ipVersion != 4 && ipVersion != 6) {
			continue
		}
		token := fmt.Sprintf("%s:%d", client, ipVersion)
		if seen[token] {
			continue
		}
		seen[token] = true
		if err := evaluateMainlandReachability(db, client, ipVersion, models.ReturnRouteTask{Client: client, IPVersion: ipVersion}, now); err != nil {
			return err
		}
	}
	return nil
}

func mainlandKeysFromTasks(tasks []models.ReturnRouteTask) [][2]any {
	keys := make([][2]any, 0, len(tasks))
	for _, task := range tasks {
		keys = append(keys, [2]any{task.Client, task.IPVersion})
	}
	return keys
}

// CleanupMainlandReachabilityData drops expired probe samples and leftover
// aggregate rows. Samples are short-lived calculation data and must not accumulate.
func CleanupMainlandReachabilityData() error {
	return cleanupMainlandReachabilityData(dbcore.GetDBInstance(), time.Now().UTC())
}

func cleanupMainlandReachabilityData(db *gorm.DB, now time.Time) error {
	cutoff := now.UTC().Add(-mainlandSampleRetention)
	if err := db.Where("checked_at < ?", cutoff).Delete(&models.ReturnRouteProbeSample{}).Error; err != nil {
		return err
	}
	return db.Where(`NOT EXISTS (
		SELECT 1 FROM return_route_tasks
		WHERE return_route_tasks.client = return_route_reachability_statuses.client
			AND return_route_tasks.ip_version = return_route_reachability_statuses.ip_version
			AND return_route_tasks.enabled = ?
			AND return_route_tasks.mainland_reachability_enabled = ?
	)`, true, true).Delete(&models.ReturnRouteReachabilityStatus{}).Error
}
