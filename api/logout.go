package api

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
)

func Logout(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	accounts.DeleteSession(session)
	c.SetCookie("session_token", "", -1, "/", "", false, true)
	c.Redirect(302, "/")
}
