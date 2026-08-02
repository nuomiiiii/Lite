package admin

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/trafficledger"
	"github.com/komari-monitor/komari/web/api"
	"gorm.io/gorm"
)

type trafficCalibrationRequest struct {
	TargetUp   int64 `json:"target_up"`
	TargetDown int64 `json:"target_down"`
}

func findTrafficCalibrationClient(uuid string) (models.Client, error) {
	var client models.Client
	err := dbcore.GetDBInstance().
		Select("uuid", "name", "traffic_reset_day").
		Where("uuid = ?", uuid).
		First(&client).Error
	return client, err
}

func GetTrafficCalibration(c *gin.Context) {
	client, err := findTrafficCalibrationClient(c.Param("uuid"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		api.RespondError(c, http.StatusNotFound, "服务器不存在")
		return
	}
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取服务器失败："+err.Error())
		return
	}
	if _, _, err := trafficledger.CurrentTrafficCycle(client.TrafficResetDay, time.Now().UTC()); err != nil {
		api.RespondSuccess(c, gin.H{
			"available": false,
			"client":    client.UUID,
			"reason":    "请先在服务器编辑中设置流量重置日",
		})
		return
	}
	snapshot, err := trafficledger.LoadCalibrationSnapshot(
		c.Request.Context(), dbcore.GetDBInstance(), client, time.Now().UTC(),
	)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取流量校准信息失败："+err.Error())
		return
	}
	api.RespondSuccess(c, gin.H{"available": true, "snapshot": snapshot})
}

func UpdateTrafficCalibration(c *gin.Context) {
	var request trafficCalibrationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.RespondError(c, http.StatusBadRequest, "流量校准参数无效："+err.Error())
		return
	}
	if request.TargetUp < 0 || request.TargetDown < 0 {
		api.RespondError(c, http.StatusBadRequest, "校准后的上传和下载流量不能小于 0")
		return
	}
	client, err := findTrafficCalibrationClient(c.Param("uuid"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		api.RespondError(c, http.StatusNotFound, "服务器不存在")
		return
	}
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取服务器失败："+err.Error())
		return
	}
	actor, _ := c.Get("uuid")
	now := time.Now().UTC()
	snapshot, err := trafficledger.CalibrateCurrentCycle(
		c.Request.Context(),
		dbcore.GetDBInstance(),
		client,
		trafficledger.Usage{Up: request.TargetUp, Down: request.TargetDown},
		fmt.Sprint(actor),
		now,
	)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "保存流量校准失败："+err.Error())
		return
	}
	auditlog.Log(
		c.ClientIP(),
		fmt.Sprint(actor),
		fmt.Sprintf("calibrated traffic for client %s to up=%d down=%d", client.UUID, request.TargetUp, request.TargetDown),
		"warn",
	)
	api.RespondSuccess(c, gin.H{"available": true, "snapshot": snapshot})
}
