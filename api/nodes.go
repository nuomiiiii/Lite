package api

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/clients"
)

func GetNodesInformation(c *gin.Context) {
	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		RespondError(c, 500, "Failed to retrieve client information: "+err.Error())
		return
	}

	count := len(clientList)
	// 公开信息不展示IP地址,私有备注不展示
	for i := 0; i < count; i++ {
		clientList[i].IPv4 = ""
		clientList[i].IPv6 = ""
		clientList[i].Remark = ""
		clientList[i].Version = ""
		clientList[i].Token = ""
	}

	RespondSuccess(c, clientList)
}
