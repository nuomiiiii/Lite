package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
)

func BindingExternalAccount(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	user, err := accounts.GetUserBySession(session)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "error": "No user found"})
		return
	}

	c.SetCookie("binding_external_account", user.UUID, 3600, "/", "", false, true)
	c.Redirect(302, "/api/oauth")
}
func UnbindExternalAccount(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	user, err := accounts.GetUserBySession(session)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "error": "No user found"})
		return
	}

	err = accounts.UnbindExternalAccount(user.UUID)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "error": "Failed to unbind external account"})
		return
	}

	c.JSON(200, gin.H{"status": "success", "message": "External account unbound"})
}
