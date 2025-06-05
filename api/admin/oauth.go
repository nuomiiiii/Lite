package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/accounts"
)

func BindingExternalAccount(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	user, err := accounts.GetUserBySession(session)
	if err != nil {
		api.RespondError(c, 500, "No user found: "+err.Error())
		return
	}

	c.SetCookie("binding_external_account", user.UUID, 3600, "/", "", false, true)
	c.Redirect(302, "/api/oauth")
}
func UnbindExternalAccount(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	user, err := accounts.GetUserBySession(session)
	if err != nil {
		api.RespondError(c, 500, "No user found: "+err.Error())
		return
	}

	err = accounts.UnbindExternalAccount(user.UUID)
	if err != nil {
		api.RespondError(c, 500, "Failed to unbind external account: "+err.Error())
		return
	}

	api.RespondSuccess(c, nil)
}
