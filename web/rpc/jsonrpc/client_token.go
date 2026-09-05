package jsonrpc

import (
	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
)

func clientWithoutToken(client models.Client) models.Client {
	client.Token = ""
	return client
}

func clientsWithoutTokens(clients []models.Client) []models.Client {
	out := make([]models.Client, len(clients))
	for i, client := range clients {
		out[i] = clientWithoutToken(client)
	}
	return out
}

func isTokenCredentialMethod(method string) bool {
	switch method {
	case "admin:addClient", "admin:getClientToken", "admin:rotateClientToken":
		return true
	default:
		return false
	}
}

func applyTokenResponseHeaders(c *gin.Context, method string) {
	if c == nil || !isTokenCredentialMethod(method) {
		return
	}
	c.Header("Cache-Control", "no-store")
}
