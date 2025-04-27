package cmd

import (
	"net/http"

	"github.com/akizon77/komari/api"
	"github.com/akizon77/komari/api/admin"
	"github.com/akizon77/komari/api/client"
	"github.com/akizon77/komari/cmd/flags"
	"github.com/akizon77/komari/ws"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the server",
	Long:  `Start the server`,
	Run: func(cmd *cobra.Command, args []string) {
		r := gin.Default()

		r.NoRoute(gin.WrapH(http.FileServer(gin.Dir("public", false))))

		r.POST("/api/login", api.Login)
		r.GET("/api/me", api.GetMe)
		r.GET("/api/clients", ws.GetClients)
		r.GET("/api/nodes", api.GetNodesInformation)

		tokenAuthrized := r.Group("/api/clients", api.TokenAuthMiddleware())
		{
			tokenAuthrized.GET("/report", client.WebSocketReport) // websocket
			tokenAuthrized.POST("/uploadBasicInfo", client.UploadBasicInfo)
			tokenAuthrized.POST("/report", client.UploadReport)
		}

		adminAuthrized := r.Group("/api/admin", api.AdminAuthMiddleware())
		{
			// clients
			adminAuthrized.POST("/addClient", admin.AddClient)
			adminAuthrized.POST("/editClient", admin.EditClient)
			adminAuthrized.GET("/listClients", admin.ListClients)
			adminAuthrized.GET("/getClient", admin.GetClient)
			adminAuthrized.POST("/clearHistory", admin.ClearHistory)
			adminAuthrized.POST("/removeClient", admin.RemoveClient)

			// settings
			adminAuthrized.GET("/settings", admin.GetSettings)
			adminAuthrized.POST("/settings", admin.EditSettings)

		}

		r.Run(flags.Listen)

	},
}

func init() {
	RootCmd.AddCommand(ServerCmd)
}
