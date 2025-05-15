package cmd

import (
	"log"
	"time"

	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/api/admin"
	"github.com/komari-monitor/komari/api/client"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/public"
	"github.com/komari-monitor/komari/utils/geoip"
	"github.com/komari-monitor/komari/ws"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the server",
	Long:  `Start the server`,
	Run: func(cmd *cobra.Command, args []string) {
		InitDatabase()

		go DoRecordsWork()

		r := gin.Default()
		cfg, err := config.Get()
		if err != nil {
			log.Fatalln("Failed to get config:", err)
		}

		if cfg.AllowCros {
			r.Use(cors.New(cors.Config{
				AllowOrigins:     []string{"*"},
				AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
				AllowHeaders:     []string{"Origin", "Content-Length", "Content-Type", "Authorization"},
				ExposeHeaders:    []string{"Content-Length"},
				AllowCredentials: true,
				MaxAge:           12 * time.Hour,
			}))
		}

		if cfg.GeoIpEnabled {
			geoip.InitGeoIp()
			go func() {
				ticker := time.NewTicker(time.Hour * 24)
				for range ticker.C {
					geoip.UpdateGeoIpDatabase()
				}
			}()
		}

		r.Use(func(c *gin.Context) {
			if len(c.Request.URL.Path) >= 4 && c.Request.URL.Path[:4] == "/api" {
				c.Header("Cache-Control", "no-store")
			}
			c.Next()
		})

		r.Any("/ping", func(c *gin.Context) {
			c.String(200, "pong")
		})

		r.POST("/api/login", api.Login)
		r.GET("/api/me", api.GetMe)
		r.GET("/api/clients", ws.GetClients)
		r.GET("/api/nodes", api.GetNodesInformation)
		r.GET("/api/public", api.GetPublicSettings)
		r.GET("/api/oauth", api.OAuth)
		r.GET("/api/oauth_callback", api.OAuthCallback)
		r.GET("/api/logout", api.Logout)
		r.GET("/api/recent/:uuid", api.GetClientRecentRecords)

		tokenAuthrized := r.Group("/api/clients", api.TokenAuthMiddleware())
		{
			tokenAuthrized.GET("/report", client.WebSocketReport) // websocket
			tokenAuthrized.POST("/uploadBasicInfo", client.UploadBasicInfo)
			tokenAuthrized.POST("/report", client.UploadReport)
		}

		adminAuthrized := r.Group("/api/admin", api.AdminAuthMiddleware())
		{
			// settings
			adminAuthrized.GET("/settings", admin.GetSettings)
			adminAuthrized.POST("/settings", admin.EditSettings)
			// clients
			clientGroup := adminAuthrized.Group("/client")
			{
				clientGroup.POST("/add", admin.AddClient)
				clientGroup.GET("/list", admin.ListClients)
				clientGroup.GET("/:uuid", admin.GetClient)
				clientGroup.POST("/:uuid/edit", admin.EditClient)
				clientGroup.POST("/:uuid/remove", admin.RemoveClient)
				clientGroup.GET("/:uuid/token", admin.GetClientToken)
				clientGroup.POST("/order", admin.OrderWeight)
			}

			// records
			recordGroup := adminAuthrized.Group("/record")
			{
				recordGroup.POST("/clear", admin.ClearRecord)
			}
			// oauth2
			oauth2Group := adminAuthrized.Group("/oauth2")
			{
				oauth2Group.GET("/bind", admin.BindingExternalAccount)
				oauth2Group.POST("/unbind", admin.UnbindExternalAccount)
			}
			sessionGroup := adminAuthrized.Group("/session")
			{
				sessionGroup.GET("/get", admin.GetSessions)
				sessionGroup.POST("/remove", admin.DeleteSession)
				sessionGroup.POST("/remove/all", admin.DeleteAllSession)
			}

		}

		public.Static(r.Group("/"), func(handlers ...gin.HandlerFunc) {
			r.NoRoute(handlers...)
		})

		go func() {
			cfg, err := config.Get()
			if err != nil {
				log.Fatalln("Failed to get config:", err)
			}
			public.UpdateIndex(cfg)
		}()

		r.Run(flags.Listen)

	},
}

func init() {
	ServerCmd.PersistentFlags().StringVarP(&flags.Listen, "listen", "l", "0.0.0.0:25774", "Listen address")
	RootCmd.AddCommand(ServerCmd)
}

func InitDatabase() {
	if !dbcore.InitDatabase() {
		user, passwd, err := accounts.CreateDefaultAdminAccount()
		if err != nil {
			panic(err)
		}
		log.Println("Default admin account created. Username:", user, ", Password:", passwd)
	}
}

func DoRecordsWork() {
	ticker := time.NewTicker(time.Minute * 30)
	ticker1 := time.NewTicker(60 * time.Second)
	records.DeleteRecordBefore(time.Now().Add(-time.Hour * 24 * 30))
	records.CompactRecord()
	for {
		select {
		case <-ticker.C:
			records.DeleteRecordBefore(time.Now().Add(-time.Hour * 24 * 30))
			records.CompactRecord()
			break
		case <-ticker1.C:
			api.SaveClientReportToDB()
			break
		}
	}

}
