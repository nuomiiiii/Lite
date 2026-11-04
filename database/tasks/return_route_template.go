package tasks

import (
	"fmt"
	"strings"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/utils"
	"gorm.io/gorm"
)

type ApplyReturnRouteTemplatesResult struct {
	Created    int `json:"created"`
	Skipped    int `json:"skipped"`
	Dispatched int `json:"dispatched"`
}

type ReturnRouteTemplateBatchEdit struct {
	IDs             []uint `json:"ids"`
	Region          string `json:"region"`
	Target          string `json:"target"`
	Interval        int    `json:"interval"`
	SwitchConfirm   int    `json:"switch_confirm"`
	RecoveryConfirm int    `json:"recovery_confirm"`
	Cooldown        int    `json:"cooldown"`
	Notify          bool   `json:"notify"`
	NotifyRecovery  bool   `json:"notify_recovery"`
	ApplyToNew      bool   `json:"apply_to_new"`
}

func ListReturnRouteTemplates() ([]models.ReturnRouteTemplate, error) {
	var templates []models.ReturnRouteTemplate
	err := dbcore.GetDBInstance().Order("id ASC").Find(&templates).Error
	return templates, err
}

func AddReturnRouteTemplate(template *models.ReturnRouteTemplate) (uint, error) {
	if err := normalizeReturnRouteTemplate(template); err != nil {
		return 0, err
	}
	db := dbcore.GetDBInstance()
	if err := ensureReturnRouteTemplateConstraints(db, template); err != nil {
		return 0, err
	}
	if err := db.Create(template).Error; err != nil {
		return 0, err
	}
	return template.Id, nil
}

func EditReturnRouteTemplate(template *models.ReturnRouteTemplate) error {
	if template.Id == 0 {
		return fmt.Errorf("template id is required")
	}
	if err := normalizeReturnRouteTemplate(template); err != nil {
		return err
	}
	db := dbcore.GetDBInstance()
	if err := ensureReturnRouteTemplateConstraints(db, template); err != nil {
		return err
	}
	result := db.Model(&models.ReturnRouteTemplate{}).Where("id = ?", template.Id).Updates(returnRouteTemplateColumnUpdates(*template))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func EditReturnRouteTemplatesBatch(params ReturnRouteTemplateBatchEdit) error {
	return editReturnRouteTemplatesBatch(dbcore.GetDBInstance(), params)
}

func editReturnRouteTemplatesBatch(db *gorm.DB, params ReturnRouteTemplateBatchEdit) error {
	ids := uniqueReturnRouteTaskIDs(params.IDs)
	if len(ids) == 0 {
		return fmt.Errorf("template ids are required")
	}
	var existing []models.ReturnRouteTemplate
	if err := db.Where("id IN ?", ids).Order("id ASC").Find(&existing).Error; err != nil {
		return err
	}
	if len(existing) != len(ids) {
		return gorm.ErrRecordNotFound
	}

	sample := existing[0]
	sample.Region = params.Region
	sample.Target = params.Target
	sample.Interval = params.Interval
	sample.SwitchConfirm = params.SwitchConfirm
	sample.RecoveryConfirm = params.RecoveryConfirm
	sample.Cooldown = params.Cooldown
	sample.Notify = params.Notify
	sample.NotifyRecovery = params.NotifyRecovery
	sample.ApplyToNew = params.ApplyToNew
	if err := normalizeReturnRouteTemplate(&sample); err != nil {
		return err
	}

	updates := map[string]any{
		"region":           sample.Region,
		"target":           sample.Target,
		"interval":         sample.Interval,
		"switch_confirm":   sample.SwitchConfirm,
		"recovery_confirm": sample.RecoveryConfirm,
		"cooldown":         sample.Cooldown,
		"notify":           sample.Notify,
		"notify_recovery":  sample.NotifyRecovery,
		"apply_to_new":     sample.ApplyToNew,
	}
	return db.Model(&models.ReturnRouteTemplate{}).Where("id IN ?", ids).Updates(updates).Error
}

func DeleteReturnRouteTemplates(ids []uint) error {
	ids = uniqueReturnRouteTaskIDs(ids)
	if len(ids) == 0 {
		return fmt.Errorf("template ids are required")
	}
	return dbcore.GetDBInstance().Where("id IN ?", ids).Delete(&models.ReturnRouteTemplate{}).Error
}

func ApplyReturnRouteTemplates(templateIDs []uint, clientUUIDs []string) (ApplyReturnRouteTemplatesResult, error) {
	result, created, err := applyReturnRouteTemplates(dbcore.GetDBInstance(), templateIDs, clientUUIDs)
	if err != nil {
		return result, err
	}
	if result.Created == 0 {
		return result, nil
	}
	if err := ReloadReturnRouteSchedule(); err != nil {
		return result, err
	}
	for _, task := range created {
		if task.Enabled && utils.DispatchReturnRouteTask(task) {
			result.Dispatched++
		}
	}
	return result, nil
}

func ApplyReturnRouteTemplatesToNewClient(uuid string) error {
	if strings.TrimSpace(uuid) == "" {
		return nil
	}
	db := dbcore.GetDBInstance()
	var templates []models.ReturnRouteTemplate
	if err := db.Where("apply_to_new = ?", true).Order("id ASC").Find(&templates).Error; err != nil {
		return err
	}
	if len(templates) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(templates))
	for _, template := range templates {
		ids = append(ids, template.Id)
	}
	_, err := ApplyReturnRouteTemplates(ids, []string{uuid})
	return err
}

func applyReturnRouteTemplates(db *gorm.DB, templateIDs []uint, clientUUIDs []string) (ApplyReturnRouteTemplatesResult, []models.ReturnRouteTask, error) {
	var result ApplyReturnRouteTemplatesResult
	templateIDs = uniqueReturnRouteTaskIDs(templateIDs)
	clients := uniqueReturnRouteClientUUIDs(clientUUIDs)
	if len(templateIDs) == 0 {
		return result, nil, fmt.Errorf("template ids are required")
	}
	if len(clients) == 0 {
		return result, nil, fmt.Errorf("clients are required")
	}

	var templates []models.ReturnRouteTemplate
	if err := db.Where("id IN ?", templateIDs).Order("id ASC").Find(&templates).Error; err != nil {
		return result, nil, err
	}
	if len(templates) != len(templateIDs) {
		return result, nil, gorm.ErrRecordNotFound
	}

	lowered := make([]string, len(clients))
	for i, uuid := range clients {
		lowered[i] = strings.ToLower(uuid)
	}
	var nodes []models.Client
	if err := db.Where("LOWER(uuid) IN ?", lowered).Find(&nodes).Error; err != nil {
		return result, nil, err
	}
	nodeByUUID := make(map[string]models.Client, len(nodes))
	for _, node := range nodes {
		nodeByUUID[strings.ToLower(node.UUID)] = node
	}
	if len(nodeByUUID) != len(clients) {
		return result, nil, fmt.Errorf("client not found")
	}

	created := make([]models.ReturnRouteTask, 0)
	for _, uuid := range clients {
		node := nodeByUUID[strings.ToLower(uuid)]
		for _, template := range templates {
			task := taskFromReturnRouteTemplate(template, node)
			if err := normalizeReturnRouteTaskWithDB(db, &task); err != nil {
				return result, created, err
			}
			task.Name = returnRouteTaskNameForClient(node.Name, template.Name, task.IPVersion)
			taken, err := returnRouteTaskSlotTaken(db, task, nil)
			if err != nil {
				return result, created, err
			}
			if taken {
				result.Skipped++
				continue
			}
			if err := db.Create(&task).Error; err != nil {
				return result, created, err
			}
			created = append(created, task)
			result.Created++
		}
	}
	return result, created, nil
}

func normalizeReturnRouteTemplate(template *models.ReturnRouteTemplate) error {
	if template == nil {
		return fmt.Errorf("template is required")
	}
	task := models.ReturnRouteTask{
		Name:            strings.TrimSpace(template.Name),
		Carrier:         template.Carrier,
		Region:          template.Region,
		Target:          template.Target,
		IPVersion:       template.IPVersion,
		ExpectedLine:    template.ExpectedLine,
		Protocol:        template.Protocol,
		Interval:        template.Interval,
		SwitchConfirm:   template.SwitchConfirm,
		RecoveryConfirm: template.RecoveryConfirm,
		Cooldown:        template.Cooldown,
		Notify:          template.Notify,
		NotifyRecovery:  template.NotifyRecovery,
		Enabled:         true,
	}
	if err := normalizeReturnRouteTaskFields(&task); err != nil {
		return err
	}
	template.Name = task.Name
	template.Carrier = task.Carrier
	template.Region = task.Region
	template.Target = task.Target
	template.IPVersion = task.IPVersion
	template.ExpectedLine = task.ExpectedLine
	template.Protocol = task.Protocol
	template.Interval = task.Interval
	template.SwitchConfirm = task.SwitchConfirm
	template.RecoveryConfirm = task.RecoveryConfirm
	template.Cooldown = task.Cooldown
	template.Notify = task.Notify
	template.NotifyRecovery = task.NotifyRecovery
	template.Enabled = true
	return nil
}

func ensureReturnRouteTemplateConstraints(db *gorm.DB, template *models.ReturnRouteTemplate) error {
	return ensureReturnRouteTemplateNameUnique(db, template)
}

func ensureReturnRouteTemplateNameUnique(db *gorm.DB, template *models.ReturnRouteTemplate) error {
	if template == nil {
		return fmt.Errorf("template is required")
	}
	name := strings.ToLower(strings.TrimSpace(template.Name))
	if name == "" {
		return fmt.Errorf("任务名称、探测目标和预期线路为必填项")
	}
	query := db.Model(&models.ReturnRouteTemplate{}).Where("LOWER(name) = ?", name)
	if template.Id != 0 {
		query = query.Where("id <> ?", template.Id)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("已有相同名称的模板")
	}
	return nil
}

func returnRouteTemplateColumnUpdates(template models.ReturnRouteTemplate) map[string]any {
	return map[string]any{
		"name":             template.Name,
		"carrier":          template.Carrier,
		"region":           template.Region,
		"target":           template.Target,
		"ip_version":       template.IPVersion,
		"expected_line":    template.ExpectedLine,
		"protocol":         template.Protocol,
		"interval":         template.Interval,
		"switch_confirm":   template.SwitchConfirm,
		"recovery_confirm": template.RecoveryConfirm,
		"cooldown":         template.Cooldown,
		"notify":           template.Notify,
		"notify_recovery":  template.NotifyRecovery,
		"enabled":          true,
		"apply_to_new":     template.ApplyToNew,
	}
}

func taskFromReturnRouteTemplate(template models.ReturnRouteTemplate, node models.Client) models.ReturnRouteTask {
	return models.ReturnRouteTask{
		Name:            returnRouteTaskNameForClient(node.Name, template.Name, template.IPVersion),
		Client:          node.UUID,
		Carrier:         template.Carrier,
		Region:          template.Region,
		Target:          template.Target,
		IPVersion:       template.IPVersion,
		ExpectedLine:    template.ExpectedLine,
		Protocol:        template.Protocol,
		Interval:        template.Interval,
		SwitchConfirm:   template.SwitchConfirm,
		RecoveryConfirm: template.RecoveryConfirm,
		Cooldown:        template.Cooldown,
		Notify:          template.Notify,
		NotifyRecovery:  template.NotifyRecovery,
		Enabled:         true,
	}
}

func returnRouteTaskNameForClient(clientName, taskName string, ipVersion int) string {
	name := strings.TrimSpace(clientName)
	if name == "" {
		name = "client"
	}
	task := strings.TrimSpace(taskName)
	if task == "" {
		task = "task"
	}
	version := "V4"
	if ipVersion == 6 {
		version = "V6"
	}
	return name + "_" + task + "_" + version
}

func uniqueReturnRouteClientUUIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}
