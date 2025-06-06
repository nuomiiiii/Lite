package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/logOperation"

	"github.com/gin-gonic/gin"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func Login(c *gin.Context) {
	conf, _ := config.Get()
	if conf.DisablePasswordLogin {
		RespondError(c, http.StatusForbidden, "Password login is disabled")
		return
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		RespondError(c, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	var data LoginRequest
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		RespondError(c, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if data.Username == "" || data.Password == "" {
		RespondError(c, http.StatusBadRequest, "Invalid request body: Username and password are required")
		return
	}

	if uuid, success := accounts.CheckPassword(data.Username, data.Password); success {

		session, err := accounts.CreateSession(uuid, 2592000)

		if err != nil {
			RespondError(c, http.StatusInternalServerError, "Failed to create session: "+err.Error())
			return
		}
		c.SetCookie("session_token", session, 2592000, "/", "", false, true)
		logOperation.Log(c.ClientIP(), uuid, "logged in (password)", "login")
		RespondSuccess(c, gin.H{"set-cookie": gin.H{"session_token": session}})
		return
	} else {
		RespondError(c, http.StatusUnauthorized, "Invalid credentials")
	}

}
func Logout(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	accounts.DeleteSession(session)
	c.SetCookie("session_token", "", -1, "/", "", false, true)
	logOperation.Log(c.ClientIP(), "", "logged out", "logout")
	c.Redirect(302, "/")
}
