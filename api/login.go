package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/config"

	"github.com/gin-gonic/gin"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func Login(c *gin.Context) {
	conf, _ := config.Get()
	if conf.DisablePasswordLogin {
		c.JSON(http.StatusForbidden, gin.H{"status": "error", "message": "Password login is disabled"})
		return
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}
	var data LoginRequest
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}
	if data.Username == "" || data.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid request body"})
		return
	}

	if uuid, success := accounts.CheckPassword(data.Username, data.Password); success {

		session, err := accounts.CreateSession(uuid, 2592000)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": "Failed to create session" + err.Error()})
			return
		}
		c.SetCookie("session_token", session, 2592000, "/", "", false, true)
		c.JSON(200, gin.H{"status": "success", "message": "", "set-cookie": map[string]interface{}{"session_token": session}})
		return
	} else {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "message": "Invalid credentials"})
	}

}
