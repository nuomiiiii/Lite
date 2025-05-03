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

		r.Any("/ping", func(c *gin.Context) {
			c.String(200, "pong")
		})

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
			adminAuthrized.POST("/clearRecord", admin.ClearRecord)
			adminAuthrized.POST("/removeClient", admin.RemoveClient)
			adminAuthrized.GET("/clientToken", admin.GetClientToken)
			// settings
			adminAuthrized.GET("/settings", admin.GetSettings)
			adminAuthrized.POST("/settings", admin.EditSettings)

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
	records.DeleteRecordBefore(time.Now().Add(-time.Hour * 24 * 30))
	records.CompactRecord()
	for range ticker.C {
		records.DeleteRecordBefore(time.Now().Add(-time.Hour * 24 * 30))
		records.CompactRecord()
	}
}
