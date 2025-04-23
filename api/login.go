package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/komari-monitor/komari/database/accounts"

	"github.com/gin-gonic/gin"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func Login(c *gin.Context) {
	// TODO: Settings -> Disable password login when using OAuth
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Invalid request body"})
		return
	}
	var data LoginRequest
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Invalid request body"})
		return
	}

	if uuid, success := accounts.CheckPassword(data.Username, data.Password); success {

		session, err := accounts.CreateSession(uuid, 2592000)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "Failed to create session" + err.Error()})
			return
		}
		c.SetCookie("session_token", session, 2592000, "/", "", false, true)
		c.JSON(200, gin.H{"set-cookie": map[string]interface{}{"session_token": session}})
		return
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "Invalid credentials"})
	}

}
