package remote

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/security"
)

var (
	errRemoteManagementDisabled = errors.New("站点未启用远程管理")
	errAgentTooOld              = errors.New("Agent 版本过旧，请升级")
	errAgentRemoteDisabled      = errors.New("节点未启用远程控制")
	errRemoteOriginDenied       = errors.New("Remote origin is not allowed")
	errRemoteQueueFull          = errors.New("远程事件队列已满，请稍后重试")
	errRemoteClientOffline      = errors.New("Client is offline")
)

func RemoteManagementEnabled() bool {
	enabled, err := config.GetAs[bool](config.AllowRemoteManagementKey, false)
	return err == nil && enabled
}

func ensureRemoteAllowed(client models.Client) error {
	if !RemoteManagementEnabled() {
		return errRemoteManagementDisabled
	}
	return AgentRemoteAllowed(client)
}

func AgentRemoteAllowed(client models.Client) error {
	if client.RemoteProtocol != 2 {
		return errAgentTooOld
	}
	if !client.RemoteControlEnabled {
		return errAgentRemoteDisabled
	}
	return nil
}

func remotePolicyStatus(err error) int {
	switch {
	case errors.Is(err, errRemoteManagementDisabled), errors.Is(err, errAgentRemoteDisabled),
		errors.Is(err, errAgentTooOld):
		return http.StatusConflict
	default:
		return http.StatusForbidden
	}
}

func rejectIfRemoteOriginDenied(c *gin.Context) bool {
	if security.RemoteOriginAllowed(c.Request) {
		return true
	}
	api.RespondError(c, http.StatusForbidden, errRemoteOriginDenied.Error())
	return false
}
