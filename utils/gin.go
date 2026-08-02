package utils

import "github.com/gin-gonic/gin"

import "github.com/komari-monitor/komari/utils/requestscheme"

// https://github.com/labstack/echo/blob/98ca08e7dd64075b858e758d6693bf9799340756/context.go#L275-L294
func GetScheme(c *gin.Context) string {
	if requestscheme.IsHTTPS(c.Request) {
		return "https"
	}
	return "http"
}

func GetCallbackURL(c *gin.Context) string {
	scheme := GetScheme(c)
	host := c.Request.Host
	return scheme + "://" + host + "/api/oauth_callback"
}
